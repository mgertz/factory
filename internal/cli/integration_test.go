//go:build linux

package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mgertz/factory/internal/paths"
	"github.com/mgertz/factory/internal/runtime"
)

// Run the actual CLI in subprocesses, including daemon re-exec, but inject a
// test-only coordination directory. Tests must never stop a user's real daemon.
// No production environment variable can override the per-user singleton lock.
func TestMain(m *testing.M) {
	if root := os.Getenv("_FACTORY_TEST_PROCESS_ROOT"); root != "" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if len(os.Args) > 1 && os.Args[1] == "__daemon" {
			switch os.Getenv("_FACTORY_TEST_DAEMON_MODE") {
			case "exit":
				os.Exit(23)
			case "stall":
				// Keep the readiness fd open without reporting readiness. The
				// starter must kill and reap this child on cancellation.
				if err := os.WriteFile(filepath.Join(root, "stalled.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
					os.Exit(24)
				}
				<-ctx.Done()
				os.Exit(25)
			}
		}
		cmd := newCommand(func() (paths.Paths, error) {
			p, err := paths.Resolve()
			p.CoordinationDir = filepath.Join(root, "k")
			return p, err
		})
		if err := cmd.ExecuteContext(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fixture struct {
	env   []string
	paths paths.Paths
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "r")
	if err := os.Mkdir(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		env: append(os.Environ(), "_FACTORY_TEST_PROCESS_ROOT="+root,
			"HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "c"),
			"XDG_STATE_HOME="+filepath.Join(root, "s"), "XDG_RUNTIME_DIR="+runtimeDir),
		paths: paths.Paths{
			ConfigFile: filepath.Join(root, "c/factory/config.yaml"), StateDir: filepath.Join(root, "s/factory"),
			RuntimeDir: filepath.Join(runtimeDir, "factory"), CoordinationDir: filepath.Join(root, "k"),
		},
	}
	t.Cleanup(func() {
		if err := runtime.Stop(context.Background(), f.paths); err != nil && !errors.Is(err, runtime.ErrNotRunning) {
			t.Errorf("cleanup stop: %v", err)
		}
	})
	return f
}

func (f *fixture) command(ctx context.Context, args ...string) *exec.Cmd {
	executable, err := os.Executable()
	if err != nil {
		panic(err)
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = f.env
	return cmd
}

func (f *fixture) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := f.command(ctx, args...).CombinedOutput()
	return string(output), err
}

func (f *fixture) must(t *testing.T, want string, args ...string) string {
	t.Helper()
	output, err := f.run(args...)
	if err != nil || !strings.Contains(output, want) {
		t.Fatalf("factory %v: %v\n%s\nwant %q", args, err, output, want)
	}
	return output
}

func TestLifecycle(t *testing.T) {
	f := newFixture(t)
	output, err := f.run("run")
	if err == nil || !strings.Contains(output, "factory config init") {
		t.Fatalf("missing config: %v: %s", err, output)
	}
	if _, err := os.Stat(f.paths.ConfigFile); !os.IsNotExist(err) {
		t.Fatal("run created missing config")
	}
	f.must(t, "Stopped", "status")
	f.must(t, "not running", "stop")
	if output, err := f.run("log"); err == nil || !strings.Contains(output, "log file not found") {
		t.Fatalf("missing log: %v: %s", err, output)
	}
	f.must(t, "Configuration created", "config", "init")
	if output, err := f.run("config", "init"); err == nil || !strings.Contains(output, "not overwritten") {
		t.Fatalf("config overwrite: %v: %s", err, output)
	}
	f.must(t, "Factory started successfully (PID:", "run")
	output = f.must(t, "Factory: Running", "status")
	for _, want := range []string{"PID:", "Uptime:", "State: Idle"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %s: %s", want, output)
		}
	}
	if output, err := f.run("run"); err == nil || !strings.Contains(output, "already running") || strings.Contains(output, "started successfully") {
		t.Fatalf("double start: %v: %s", err, output)
	}
	f.must(t, "Factory started", "log")
	f.must(t, "Factory started", "log", "-n", "1")
	f.must(t, "Factory stopped.", "stop")
	f.must(t, "Factory: Stopped", "status")
	f.must(t, "Factory stopping", "log")
	f.must(t, "started successfully", "run")
	f.must(t, "Factory stopped.", "stop")
}

func TestInvalidConfig(t *testing.T) {
	f := newFixture(t)
	f.must(t, "created", "config", "init")
	if err := os.WriteFile(f.paths.ConfigFile, []byte("logging: ["), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"run"}, {"run", "--foreground"}} {
		if output, err := f.run(args...); err == nil || !strings.Contains(output, "invalid configuration") {
			t.Fatalf("invalid config: %v: %s", err, output)
		}
	}
	f.must(t, "Stopped", "status")
}

func TestStartFailure(t *testing.T) {
	f := newFixture(t)
	f.must(t, "created", "config", "init")
	// Config is valid, but the child cannot open its log. Parent must report
	// the child's failure, not a successful process spawn.
	if err := os.MkdirAll(f.paths.LogFile(), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := f.run("run")
	if err == nil || strings.Contains(output, "started successfully") || !strings.Contains(output, "factory.log") {
		t.Fatalf("child startup failure: %v: %s", err, output)
	}
	f.must(t, "Stopped", "status")
	if err := os.Remove(f.paths.LogFile()); err != nil {
		t.Fatal(err)
	}
	f.must(t, "started successfully", "run")
}

func TestExitBeforeReadiness(t *testing.T) {
	f := newFixture(t)
	f.must(t, "created", "config", "init")
	f.env = append(f.env, "_FACTORY_TEST_DAEMON_MODE=exit")
	output, err := f.run("run")
	if err == nil || !strings.Contains(output, "exited without readiness") || strings.Contains(output, "started successfully") {
		t.Fatalf("unready child exit: %v: %s", err, output)
	}
	f.must(t, "Stopped", "status")
}

func TestCancelStartup(t *testing.T) {
	f := newFixture(t)
	f.must(t, "created", "config", "init")
	f.env = append(f.env, "_FACTORY_TEST_DAEMON_MODE=stall")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := f.command(ctx, "run")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	marker := filepath.Join(filepath.Dir(f.paths.CoordinationDir), "stalled.pid")
	deadline := time.Now().Add(5 * time.Second)
	var child *os.Process
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			child, err = os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not reach readiness stall")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { child.Kill(); child.Release() })
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil || !strings.Contains(output.String(), "startup did not complete") {
		t.Fatalf("cancelled startup: %v: %s", err, output.String())
	}
	if err := child.Signal(syscall.Signal(0)); err == nil {
		t.Fatal("cancelled startup left its child alive")
	}
	f.must(t, "Stopped", "status")
}

func TestCrash(t *testing.T) {
	f := newFixture(t)
	f.must(t, "created", "config", "init")
	f.must(t, "started successfully", "run")
	status, err := runtime.Inspect(context.Background(), f.paths)
	if err != nil {
		t.Fatal(err)
	}
	// This PID was obtained from our own freshly started test instance.
	if err := syscall.Kill(status.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := runtime.Inspect(context.Background(), f.paths)
		if errors.Is(err, runtime.ErrNotRunning) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("crash not detected: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Lstat(f.paths.Socket()); err != nil {
		t.Fatal("expected a stale socket after SIGKILL:", err)
	}
	f.must(t, "Stopped", "status")
	f.must(t, "not running", "stop")
	f.must(t, "started successfully", "run")
	f.must(t, "Running", "status")
}

func TestForeground(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			f := newFixture(t)
			f.must(t, "created", "config", "init")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := f.command(ctx, "run", "--foreground")
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill() })
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := runtime.Inspect(ctx, f.paths); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("foreground startup timed out")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("foreground exit: %v: %s", err, output.String())
			}
			if !strings.Contains(output.String(), "Factory started") || !strings.Contains(output.String(), "Factory stopping") {
				t.Fatalf("foreground logs missing: %s", output.String())
			}
			f.must(t, "Stopped", "status")
			f.must(t, "Factory stopping", "log")
		})
	}
}

func TestOtherEnvironment(t *testing.T) {
	f := newFixture(t)
	f.must(t, "created", "config", "init")
	f.must(t, "started successfully", "run")
	other := t.TempDir()
	if err := os.Chmod(other, 0700); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "XDG_CONFIG_HOME="+filepath.Join(other, "config"), "XDG_STATE_HOME="+filepath.Join(other, "state"), "XDG_RUNTIME_DIR="+other)
	f.must(t, "created", "config", "init")
	if output, err := f.run("run"); err == nil || !strings.Contains(output, "already running") {
		t.Fatalf("different environment bypassed per-user lock: %v: %s", err, output)
	}
	f.must(t, "Running", "status")
	f.must(t, "stopped", "stop")
}

func TestConcurrentStart(t *testing.T) {
	f := newFixture(t)
	f.must(t, "created", "config", "init")
	type result struct {
		output string
		err    error
	}
	results := make(chan result, 4)
	for range 4 {
		go func() {
			output, err := f.run("run")
			results <- result{output, err}
		}()
	}
	successes := 0
	for range 4 {
		r := <-results
		if r.err == nil {
			successes++
		} else if !strings.Contains(r.output, "already running") {
			t.Errorf("unexpected concurrent start error: %v: %s", r.err, r.output)
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent starts: %d", successes)
	}
}

func TestLogLines(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.paths.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&text, "line %d\n", i)
	}
	if err := os.WriteFile(f.paths.LogFile(), []byte(text.String()), 0600); err != nil {
		t.Fatal(err)
	}
	output := f.must(t, "line 51\n", "log")
	if strings.Count(output, "\n") != 50 {
		t.Fatalf("default log lines: %d", strings.Count(output, "\n"))
	}
	output = f.must(t, "line 1\n", "log", "-n", "100")
	if strings.Count(output, "\n") != 100 {
		t.Fatal("-n 100 did not return 100 lines")
	}
	if output, err := f.run("log", "-n", "-1"); err == nil || !strings.Contains(output, "must not be negative") {
		t.Fatalf("negative line count: %v: %s", err, output)
	}
}

func TestStatusUsesStdout(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := f.command(ctx, "status")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil || !strings.Contains(string(stdout), "Factory: Stopped") || stderr.Len() != 0 {
		t.Fatalf("status stdout = %q, stderr = %q, error = %v", stdout, stderr.String(), err)
	}
}

func TestLogFollow(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.paths.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.paths.LogFile(), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := f.command(ctx, "log", "-n", "1", "-f")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	lines := make(chan string, 3)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	receive := func(want string) {
		t.Helper()
		select {
		case got := <-lines:
			if got != want {
				t.Fatalf("log follow = %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatal("log follow timed out")
		}
	}
	receive("old")
	file, err := os.OpenFile(f.paths.LogFile(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("new\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	receive("new")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal("interrupted log follow:", err)
	}
}

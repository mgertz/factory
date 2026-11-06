//go:build linux

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/mgertz/factory/internal/config"
	"github.com/mgertz/factory/internal/ipc"
	"github.com/mgertz/factory/internal/paths"
)

type startupResult struct {
	Status ipc.Status `json:"status"`
	Error  string     `json:"error,omitempty"`
}

func Start(ctx context.Context, p paths.Paths) (ipc.Status, error) {
	// Fast failure without creating config, log, or runtime directories.
	if _, err := config.Load(p.ConfigFile); err != nil {
		return ipc.Status{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return ipc.Status{}, err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return ipc.Status{}, err
	}
	defer read.Close()
	defer write.Close()
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return ipc.Status{}, err
	}
	defer devnull.Close()
	cmd := exec.Command(executable, "__daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	cmd.ExtraFiles = []*os.File{write} // Child fd 3 is the private readiness pipe.
	if err := cmd.Start(); err != nil {
		return ipc.Status{}, fmt.Errorf("start background process: %w", err)
	}
	write.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	results := make(chan startupResult, 1)
	go func() {
		var result startupResult
		if err := json.NewDecoder(io.LimitReader(read, 8192)).Decode(&result); err != nil {
			result.Error = fmt.Sprintf("background process exited without readiness: %v", err)
		}
		results <- result
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var startupErr error
	select {
	case result := <-results:
		if result.Error == "" && result.Status.PID == cmd.Process.Pid && result.Status.InstanceID != "" {
			if ctx.Err() == nil {
				return result.Status, nil
			}
			result.Error = fmt.Sprintf("background startup cancelled: %v", ctx.Err())
		}
		if result.Error == "" {
			result.Error = "invalid background readiness response"
		}
		startupErr = errors.New(result.Error)
	case <-ctx.Done():
		startupErr = fmt.Errorf("background startup did not complete: %w", ctx.Err())
	}
	// Only ever kill the child we just spawned, never a PID read from disk.
	cmd.Process.Kill()
	<-exited
	return ipc.Status{}, startupErr
}

// Child is only used by the hidden re-exec command. It must have a readiness
// pipe; accidentally invoking the internal command must not start a daemon.
func Child(ctx context.Context, resolve func() (paths.Paths, error)) error {
	pipe := os.NewFile(3, "factory-readiness")
	if pipe == nil {
		return errors.New("internal daemon command requires a readiness pipe")
	}
	defer pipe.Close()
	info, err := pipe.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("internal daemon command requires a readiness pipe")
	}
	reported := false
	report := func(result startupResult) error {
		reported = true
		err := json.NewEncoder(pipe).Encode(result)
		pipe.Close()
		return err
	}
	p, err := resolve()
	if err == nil {
		err = Serve(ctx, p, nil, func(status ipc.Status) error {
			return report(startupResult{Status: status})
		})
	}
	if err != nil && !reported {
		if reportErr := report(startupResult{Error: err.Error()}); reportErr != nil {
			return errors.Join(err, reportErr)
		}
	}
	return err
}

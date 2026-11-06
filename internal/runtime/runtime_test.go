package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mgertz/factory/internal/config"
	"github.com/mgertz/factory/internal/ipc"
	"github.com/mgertz/factory/internal/paths"
)

func testPaths(t *testing.T) paths.Paths {
	t.Helper()
	root := t.TempDir()
	p := paths.Paths{
		ConfigFile: filepath.Join(root, "c/config.yaml"), StateDir: filepath.Join(root, "s"),
		RuntimeDir: filepath.Join(root, "r"), CoordinationDir: filepath.Join(root, "k"),
	}
	if err := config.Init(p.ConfigFile); err != nil {
		t.Fatal(err)
	}
	return p
}

func serve(t *testing.T, p paths.Paths) ipc.Status {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan ipc.Status, 1)
	done := make(chan struct{})
	var serveErr error
	go func() {
		serveErr = Serve(ctx, p, nil, func(s ipc.Status) error { ready <- s; return nil })
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			if serveErr != nil {
				t.Errorf("Serve: %v", serveErr)
			}
		case <-time.After(5 * time.Second):
			t.Error("runtime failed to exit")
		}
	})
	select {
	case status := <-ready:
		return status
	case <-done:
		t.Fatalf("startup failed: %v", serveErr)
	case <-time.After(5 * time.Second):
		t.Fatal("startup timed out")
	}
	return ipc.Status{}
}

func TestLifecycle(t *testing.T) {
	p := testPaths(t)
	want := serve(t, p)
	got, err := Inspect(context.Background(), p)
	if err != nil || got.InstanceID != want.InstanceID || got.PID != os.Getpid() || got.State != "Idle" {
		t.Fatalf("Inspect = %+v, %v", got, err)
	}
	if err := Serve(context.Background(), p, nil, nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("double start = %v", err)
	}
	if _, err := ipc.Call(context.Background(), p.Socket(), ipc.Request{Command: "stop", InstanceID: "wrong"}); err == nil {
		t.Fatal("accepted stop for a different instance")
	}
	if _, err := Inspect(context.Background(), p); err != nil {
		t.Fatal("incorrect stop terminated the process:", err)
	}
	if err := Stop(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), p); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("status after stop = %v", err)
	}
	if _, err := os.Stat(p.LockFile()); err != nil {
		t.Fatal("lock file must remain on disk:", err)
	}
	if _, err := os.Lstat(p.Socket()); !os.IsNotExist(err) {
		t.Fatal("socket not removed on shutdown")
	}
	serve(t, p)
}

func TestStaleSocket(t *testing.T) {
	p := testPaths(t)
	if err := paths.EnsureDir(p.RuntimeDir); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: p.Socket(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	listener.Close()
	if _, err := Inspect(context.Background(), p); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stale socket reported as live: %v", err)
	}
	serve(t, p)
}

func TestNonSocketIsPreserved(t *testing.T) {
	p := testPaths(t)
	if err := paths.EnsureDir(p.RuntimeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Socket(), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), p, nil, nil); err == nil {
		t.Fatal("accepted a regular file at the socket path")
	}
	data, _ := os.ReadFile(p.Socket())
	if string(data) != "keep" {
		t.Fatal("overwrote an unexpected file")
	}
	if held, err := locked(p); held || err != nil {
		t.Fatalf("startup failure leaked lock: %v, %v", held, err)
	}
}

func TestReadinessFailure(t *testing.T) {
	p := testPaths(t)
	want := errors.New("parent disappeared")
	if err := Serve(context.Background(), p, nil, func(ipc.Status) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if held, err := locked(p); held || err != nil {
		t.Fatalf("readiness failure leaked lock: %v, %v", held, err)
	}
	if _, err := os.Stat(p.EndpointFile()); !os.IsNotExist(err) {
		t.Fatal("readiness failure leaked discovery record")
	}
	serve(t, p)
}

func TestUnresponsiveIsNotStopped(t *testing.T) {
	p := testPaths(t)
	if err := paths.EnsureDir(p.CoordinationDir); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLock(p, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := Inspect(context.Background(), p); err == nil || errors.Is(err, ErrNotRunning) {
		t.Fatalf("locked but unresponsive instance should be an error: %v", err)
	}
}

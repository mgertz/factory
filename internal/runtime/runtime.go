//go:build linux

// Package runtime owns the single local Factory process and its lifecycle.
package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mgertz/factory/internal/config"
	"github.com/mgertz/factory/internal/ipc"
	"github.com/mgertz/factory/internal/logging"
	"github.com/mgertz/factory/internal/paths"
	"golang.org/x/sys/unix"
)

var (
	ErrAlreadyRunning = errors.New("Factory is already running (or starting) for this user")
	ErrNotRunning     = errors.New("Factory is not running")
)

func acquireLock(p paths.Paths, create bool) (*os.File, error) {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	f, err := paths.OpenFile(p.LockFile(), flags)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return f, nil
}

func locked(p paths.Paths) (bool, error) {
	if err := paths.CheckDir(p.CoordinationDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	f, err := acquireLock(p, false)
	if errors.Is(err, ErrAlreadyRunning) {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	f.Close()
	return false, nil
}

// Serve is shared by foreground and daemon mode. The lock is the last resource
// released, so stop can observe completion without ever sending a PID a signal.
func Serve(ctx context.Context, p paths.Paths, terminal io.Writer, ready func(ipc.Status) error) error {
	cfg, err := config.Load(p.ConfigFile)
	if err != nil {
		return err
	}
	if err := paths.EnsureDir(p.RuntimeDir); err != nil {
		return err
	}
	if err := paths.EnsureDir(p.CoordinationDir); err != nil {
		return err
	}
	lock, err := acquireLock(p, true)
	if err != nil {
		return err
	}
	defer lock.Close() // Never unlink the lock file: flock locks the inode.
	level, _ := cfg.Level()
	logger, logfile, err := logging.Open(p, level, terminal)
	if err != nil {
		return err
	}
	defer logfile.Close()
	if info, err := os.Lstat(p.Socket()); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to remove non-socket runtime file: %s", p.Socket())
		}
		if err := os.Remove(p.Socket()); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	status := ipc.Status{PID: os.Getpid(), StartedAt: time.Now(), State: "Idle", InstanceID: rand.Text()}
	server, err := ipc.Listen(p.Socket(), status, cancel)
	if err != nil {
		return fmt.Errorf("create control socket %s: %w", p.Socket(), err)
	}
	defer func() {
		server.Close()
		if err := os.Remove(p.Socket()); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Error("remove control socket", "error", err)
		}
	}()
	if err := writeEndpoint(p, endpoint{Socket: p.Socket(), InstanceID: status.InstanceID}); err != nil {
		return fmt.Errorf("publish control socket: %w", err)
	}
	defer os.Remove(p.EndpointFile())
	if err := ctx.Err(); err != nil {
		return err
	}
	logger.Info("Factory started", "pid", status.PID, "state", status.State)
	logger.Debug("Runtime ready", "socket", p.Socket())
	if ready != nil {
		if err := ready(status); err != nil {
			return fmt.Errorf("report startup readiness: %w", err)
		}
	}
	<-ctx.Done()
	logger.Info("Factory stopping")
	return nil
}

func Inspect(ctx context.Context, p paths.Paths) (ipc.Status, error) {
	status, _, err := inspect(ctx, p)
	return status, err
}

func inspect(ctx context.Context, p paths.Paths) (ipc.Status, string, error) {
	held, err := locked(p)
	if err != nil {
		return ipc.Status{}, "", err
	}
	if !held {
		return ipc.Status{}, "", ErrNotRunning
	}
	e, err := readEndpoint(p)
	var status ipc.Status
	if err == nil {
		status, err = ipc.Call(ctx, e.Socket, ipc.Request{Command: "status"})
		if err == nil && status.InstanceID != e.InstanceID {
			err = errors.New("Factory endpoint does not match the active instance")
		}
	}
	if err != nil {
		// The process may have exited between the lock probe and request.
		if held, probeErr := locked(p); probeErr == nil && !held {
			return ipc.Status{}, "", ErrNotRunning
		}
		return ipc.Status{}, "", fmt.Errorf("Factory holds its process lock but status is unavailable: %w", err)
	}
	return status, e.Socket, nil
}

func Stop(ctx context.Context, p paths.Paths) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, socket, err := inspect(ctx, p)
	if err != nil {
		return err
	}
	if _, err := ipc.Call(ctx, socket, ipc.Request{Command: "stop", InstanceID: status.InstanceID}); err != nil {
		return fmt.Errorf("request shutdown: %w", err)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, err := locked(p)
		if err != nil {
			return err
		}
		if !held {
			return nil
		}
		// A replacement instance must not make us wait for (or stop) it.
		current, err := Inspect(ctx, p)
		if err == nil && current.InstanceID != status.InstanceID {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("shutdown not confirmed: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

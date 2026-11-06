//go:build linux

// Package paths resolves user-owned Factory storage and checks private files.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

type Paths struct {
	ConfigFile      string
	StateDir        string
	RuntimeDir      string
	CoordinationDir string
}

func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve HOME: %w", err)
	}
	config := xdgHome("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	state := xdgHome("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	coordination := filepath.Join("/tmp", "factory-"+strconv.Itoa(os.Getuid()))
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime != "" {
		if !filepath.IsAbs(runtime) {
			return Paths{}, errors.New("XDG_RUNTIME_DIR must be an absolute path")
		}
		// The base must already be supplied securely by the login environment.
		if err := CheckDir(runtime); err != nil {
			return Paths{}, fmt.Errorf("invalid XDG_RUNTIME_DIR: %w", err)
		}
		runtime = filepath.Join(runtime, "factory")
	} else {
		// Deliberately independent of TMPDIR: all shells share the same fallback.
		runtime = coordination
	}
	return Paths{
		ConfigFile:      filepath.Join(config, "factory", "config.yaml"),
		StateDir:        filepath.Join(state, "factory"),
		RuntimeDir:      runtime,
		CoordinationDir: coordination,
	}, nil
}

func xdgHome(key, fallback string) string {
	if value := os.Getenv(key); filepath.IsAbs(value) {
		return value
	}
	return fallback
}

func (p Paths) LogFile() string      { return filepath.Join(p.StateDir, "factory.log") }
func (p Paths) LockFile() string     { return filepath.Join(p.CoordinationDir, "instance.lock") }
func (p Paths) EndpointFile() string { return filepath.Join(p.CoordinationDir, "endpoint.json") }
func (p Paths) Socket() string       { return filepath.Join(p.RuntimeDir, "control.sock") }

func EnsureDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("create private directory %s: %w", path, err)
	}
	return CheckDir(path)
}

func CheckDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory, not a symlink or other file", path)
	}
	return checkOwnerMode(path, info)
}

func checkOwnerMode(path string, info os.FileInfo) error {
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%s must be owned by UID %d and inaccessible to other users", path, os.Getuid())
	}
	return nil
}

// OpenFile refuses symlinks, non-regular files, shared permissions and hard links.
// O_NONBLOCK avoids hanging if a malicious/stale path is a FIFO.
func OpenFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil {
		if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 {
			err = fmt.Errorf("%s must be a regular file with one link", path)
		} else {
			err = checkOwnerMode(path, info)
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

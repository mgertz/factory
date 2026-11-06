package paths

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	p, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigFile != filepath.Join(home, ".config/factory/config.yaml") || p.StateDir != filepath.Join(home, ".local/state/factory") {
		t.Fatalf("unexpected defaults: %+v", p)
	}
	wantFallback := "/tmp/factory-" + strconv.Itoa(os.Getuid())
	if p.RuntimeDir != wantFallback || p.CoordinationDir != wantFallback {
		t.Fatalf("unexpected fallback: %+v", p)
	}
	xdg := t.TempDir()
	if err := os.Chmod(xdg, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "c"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(xdg, "s"))
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	p, err = Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigFile != filepath.Join(xdg, "c/factory/config.yaml") || p.StateDir != filepath.Join(xdg, "s/factory") || p.RuntimeDir != filepath.Join(xdg, "factory") {
		t.Fatalf("unexpected XDG paths: %+v", p)
	}
	if p.CoordinationDir != wantFallback {
		t.Fatal("XDG environment changed the per-user lock location")
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("XDG_STATE_HOME", "relative")
	p, err = Resolve()
	if err != nil || p.ConfigFile != filepath.Join(home, ".config/factory/config.yaml") {
		t.Fatalf("relative XDG home should be ignored: %+v, %v", p, err)
	}
}

func TestPrivateDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "private")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(link); err == nil {
		t.Fatal("accepted a symlink directory")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir); err == nil {
		t.Fatal("accepted a shared directory")
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if _, err := Resolve(); err == nil {
		t.Fatal("accepted an insecure XDG_RUNTIME_DIR")
	}
}

func TestPrivateFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	f, err := OpenFile(path, os.O_RDWR|os.O_CREATE)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenFile(link, os.O_RDONLY); err == nil {
		f.Close()
		t.Fatal("accepted symlink")
	}
	hardlink := filepath.Join(root, "hardlink")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenFile(path, os.O_RDONLY); err == nil {
		f.Close()
		t.Fatal("accepted hard-linked file")
	}
	if err := os.Remove(hardlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenFile(path, os.O_RDONLY); err == nil {
		f.Close()
		t.Fatal("accepted world-readable file")
	}
}

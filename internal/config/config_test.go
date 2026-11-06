package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory", "config.yaml")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if !strings.Contains(string(before), "# Minimum log level") {
		t.Fatal("template is not commented")
	}
	if err := Init(path); err == nil {
		t.Fatal("overwrote existing config")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("existing config changed")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
}

func TestConcurrentInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory", "config.yaml")
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if Init(path) == nil {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful initializations = %d", successes.Load())
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name, yaml, wantError string
	}{
		{"debug", "logging:\n  level: debug\n", ""},
		{"info", "logging:\n  level: info\n", ""},
		{"warn", "logging:\n  level: warn\n", ""},
		{"error", "logging:\n  level: error\n", ""},
		{"syntax", "logging: [", "invalid configuration"},
		{"unknown root", "github: {}\nlogging:\n  level: info", "field github"},
		{"unknown nested", "logging:\n  level: info\n  destination: stderr", "field destination"},
		{"invalid level", "logging:\n  level: trace", "logging.level"},
		{"missing level", "logging: {}", "logging.level"},
		{"null", "null", "logging.level"},
		{"empty", "", "invalid configuration"},
		{"duplicate", "logging:\n  level: info\n  level: debug", "already defined"},
		{"multiple documents", "logging:\n  level: info\n---\nlogging:\n  level: debug", "exactly one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("got %v, want error containing %q", err, tt.wantError)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if _, err := cfg.Level(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "factory config init") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Load created the missing config")
	}
}

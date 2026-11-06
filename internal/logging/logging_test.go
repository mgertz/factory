package logging

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mgertz/factory/internal/paths"
)

func TestTail(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		n          int
		want       string
	}{
		{"empty", "", 50, ""},
		{"zero", "a\nb\n", 0, ""},
		{"all", "a\nb\n", 50, "a\nb\n"},
		{"last", "a\nb\nc\n", 2, "b\nc\n"},
		{"partial", "a\nb\nc", 2, "b\nc"},
		{"one", "a", 1, "a"},
		{"blank", "a\n\n", 1, "\n"},
		{"block boundary", strings.Repeat("x", 70000) + "\nb\nc\n", 2, "b\nc\n"},
		{"long line", "a\n" + strings.Repeat("x", 70000), 1, strings.Repeat("x", 70000)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "log")
			if err := os.WriteFile(path, []byte(tt.text), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			var output bytes.Buffer
			offset, err := Tail(f, &output, tt.n)
			if err != nil || output.String() != tt.want || offset != int64(len(tt.text)) {
				t.Fatalf("Tail = %q, %d, %v; want %q", output.String(), offset, err, tt.want)
			}
		})
	}
}

type channelWriter chan string

func (c channelWriter) Write(p []byte) (int, error) {
	c <- string(p)
	return len(p), nil
}

func TestFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := make(channelWriter, 10)
	done := make(chan error, 1)
	go func() { done <- Show(ctx, path, output, 1, true) }()
	receive := func(want string) {
		t.Helper()
		select {
		case got := <-output:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	receive("old\n")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("new\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	receive("new\n")
	if err := os.WriteFile(path, []byte("x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	receive("x\n")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("follow ignored cancellation")
	}
}

func TestLevelFiltering(t *testing.T) {
	p := paths.Paths{StateDir: filepath.Join(t.TempDir(), "state")}
	var terminal bytes.Buffer
	logger, file, err := Open(p, slog.LevelWarn, &terminal)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("hidden")
	logger.Warn("visible")
	file.Close()
	data, err := os.ReadFile(p.LogFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hidden") || !strings.Contains(string(data), "level=WARN") || !strings.Contains(string(data), "time=") {
		t.Fatalf("unexpected log: %s", data)
	}
	if terminal.String() != string(data) {
		t.Fatal("terminal and file logging differ")
	}
}

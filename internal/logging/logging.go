package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/mgertz/factory/internal/paths"
)

func Open(p paths.Paths, level slog.Level, terminal io.Writer) (*slog.Logger, *os.File, error) {
	if err := paths.EnsureDir(p.StateDir); err != nil {
		return nil, nil, err
	}
	f, err := paths.OpenFile(p.LogFile(), os.O_WRONLY|os.O_CREATE|os.O_APPEND)
	if err != nil {
		return nil, nil, err
	}
	var output io.Writer = f
	if terminal != nil {
		output = io.MultiWriter(f, terminal)
	}
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: level})), f, nil
}

// Tail copies the last n lines from a snapshot of f and returns the snapshot's
// end offset. It scans backwards in fixed-size blocks, without loading the file.
func Tail(f *os.File, output io.Writer, n int) (int64, error) {
	if n < 0 {
		return 0, errors.New("line count must not be negative")
	}
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	end := info.Size()
	if n == 0 || end == 0 {
		return end, nil
	}
	buffer := make([]byte, 32*1024)
	position, start, lines := end, int64(0), 0
search:
	for position > 0 {
		size := min(int64(len(buffer)), position)
		position -= size
		if _, err := f.ReadAt(buffer[:size], position); err != nil {
			return 0, err
		}
		for i := size - 1; i >= 0; i-- {
			if position+i == end-1 && buffer[i] == '\n' {
				continue
			}
			if buffer[i] == '\n' {
				lines++
				if lines == n {
					start = position + i + 1
					break search
				}
			}
		}
	}
	_, err = io.Copy(output, io.NewSectionReader(f, start, end-start))
	return end, err
}

func Show(ctx context.Context, path string, output io.Writer, n int, follow bool) error {
	f, err := paths.OpenFile(path, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("log file not found: %s; start Factory to create it", path)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	offset, err := Tail(f, output, n)
	if err != nil || !follow {
		return err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			info, err := f.Stat()
			if err != nil {
				return err
			}
			if info.Size() < offset {
				if _, err := f.Seek(0, io.SeekStart); err != nil {
					return err
				}
				offset = 0
			}
			written, err := io.Copy(output, f)
			if err != nil {
				return err
			}
			offset += written
		}
	}
}

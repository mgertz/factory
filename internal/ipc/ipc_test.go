package ipc

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	want := Status{PID: os.Getpid(), StartedAt: time.Now(), State: "Idle", InstanceID: "test-instance"}
	stopped := make(chan struct{}, 1)
	server, err := Listen(path, want, func() { stopped <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v, %v", info, err)
	}
	status, err := Call(context.Background(), path, Request{Command: "status"})
	if err != nil || status.InstanceID != want.InstanceID || !status.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if _, err := Call(context.Background(), path, Request{Command: "anything"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unknown command = %v", err)
	}
	if _, err := Call(context.Background(), path, Request{Command: "stop", InstanceID: "wrong"}); err == nil {
		t.Fatal("incorrect instance identity accepted")
	}
	select {
	case <-stopped:
		t.Fatal("invalid request stopped the server")
	default:
	}
	if _, err := Call(context.Background(), path, Request{Command: "stop", InstanceID: want.InstanceID}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop callback was not called")
	}
}

func TestMalformedRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server, err := Listen(path, Status{}, func() { t.Error("invalid request stopped the server") })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("{\"unknown_field\": true}\n")); err != nil {
		t.Fatal(err)
	}
	if n, _ := conn.Read(make([]byte, 100)); n != 0 {
		t.Fatal("malformed request received a valid response")
	}
}

func TestCancelledCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Call(ctx, filepath.Join(t.TempDir(), "missing.sock"), Request{Command: "status"}); err == nil {
		t.Fatal("cancelled request unexpectedly succeeded")
	}
}

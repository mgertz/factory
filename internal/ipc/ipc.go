//go:build linux

// Package ipc provides a small, local-only status and shutdown protocol.
package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const Timeout = 2 * time.Second

type Status struct {
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	State      string    `json:"state"`
	InstanceID string    `json:"instance_id"`
}

type Request struct {
	Command    string `json:"command"`
	InstanceID string `json:"instance_id,omitempty"`
}

type Response struct {
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

type Server struct {
	listener *net.UnixListener
	wg       sync.WaitGroup
}

func Listen(path string, status Status, stop func()) (*Server, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// The runtime owns unlinking while it holds its process lock.
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, err
	}
	s := &Server{listener: listener}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(Timeout))
				if err := sameUser(conn); err != nil {
					return
				}
				var req Request
				decoder := json.NewDecoder(io.LimitReader(conn, 4096))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&req); err != nil {
					return
				}
				response := Response{Status: status}
				switch req.Command {
				case "status":
				case "stop":
					if req.InstanceID != status.InstanceID {
						response.Error = "Factory instance changed; retry the command"
					}
				default:
					response.Error = "unknown command"
				}
				if err := json.NewEncoder(conn).Encode(response); err == nil && req.Command == "stop" && response.Error == "" {
					stop()
				}
			}()
		}
	}()
	return s, nil
}

func (s *Server) Close() error {
	err := s.listener.Close()
	s.wg.Wait()
	return err
}

func Call(ctx context.Context, path string, request Request) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return Status{}, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	if err := sameUser(conn.(*net.UnixConn)); err != nil {
		return Status{}, err
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return Status{}, err
	}
	var response Response
	if err := json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&response); err != nil {
		return Status{}, err
	}
	if response.Error != "" {
		return Status{}, errors.New(response.Error)
	}
	if response.Status.PID <= 0 || response.Status.StartedAt.IsZero() || response.Status.InstanceID == "" || response.Status.State != "Idle" {
		return Status{}, errors.New("invalid Factory status response")
	}
	return response.Status, nil
}

func sameUser(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var credentials *unix.Ucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credentialErr != nil {
		return credentialErr
	}
	if credentials.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("Unix socket peer is not owned by UID %d", os.Getuid())
	}
	return nil
}

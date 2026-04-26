// Package spawn manages a single user-function instance and the Unix
// socket it talks to. Two modes are supported:
//
//   - Start: spawns the user binary as a direct subprocess on the host.
//     Used for Phase-1 dev and on macOS.
//   - StartRunc: starts the user binary inside an OCI container via runc.
//     Used in production and on Linux dev VMs.
//
// Both return an *Instance with the same shape so the gateway can stay
// agnostic to the underlying mechanism.
package spawn

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/fabianringel/cfunc/internal/wire"
)

// Instance is one running user-function process plus its socket connection.
// Lifecycle hooks (stop/wait) are populated by the spawner and called by
// Close in order: Close socket -> stop runtime -> wait for exit -> remove
// temp directories.
type Instance struct {
	conn    net.Conn
	listen  net.Listener
	sockDir string

	// ColdStartDuration is the wall time from spawn start to socket accept.
	ColdStartDuration time.Duration
	// Mode names the spawner that created this instance ("process" or "runc").
	Mode string

	// stop terminates the underlying runtime (process or container) and is
	// expected to be safe to call once.
	stop func() error
	// wait blocks until the runtime has fully exited and any resources
	// associated with it are reclaimed.
	wait func()

	mu sync.Mutex
}

// Start spawns binary as a host subprocess, waits for it to dial back on
// a fresh Unix socket, and returns a ready Instance.
func Start(binary string, env []string, dialTimeout time.Duration) (*Instance, error) {
	t0 := time.Now()
	sockDir, sockPath, ln, err := makeSocket()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(binary)
	// Curated env inheritance. We intentionally drop everything the user
	// didn't ask for: a compromised function shouldn't see secrets like
	// CFUNC_ADMIN_TOKEN, AWS_*, KUBERNETES_*, etc. just because they're
	// in the gateway's environment. PATH stays so shebang interpreters
	// resolve. Caller-supplied entries layer on top, then CFUNC_SOCKET.
	cmd.Env = append(append(inheritedEnv(), env...), "CFUNC_SOCKET="+sockPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		ln.Close()
		os.RemoveAll(sockDir)
		return nil, fmt.Errorf("spawn: start %s: %w", binary, err)
	}

	conn, err := acceptOrKill(ln, dialTimeout, func() { _ = cmd.Process.Kill() })
	if err != nil {
		ln.Close()
		os.RemoveAll(sockDir)
		return nil, err
	}

	return &Instance{
		conn: conn, listen: ln, sockDir: sockDir,
		Mode:              "process",
		ColdStartDuration: time.Since(t0),
		stop: func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		},
		wait: func() { _ = cmd.Wait() },
	}, nil
}

// Invoke sends one invoke frame and blocks for the matching reply.
// Not safe for concurrent use; the gateway serializes per-instance.
func (i *Instance) Invoke(f *wire.Frame) (*wire.Frame, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := wire.WriteFrame(i.conn, f); err != nil {
		return nil, err
	}
	return wire.ReadFrame(i.conn)
}

// Close shuts the instance down: graceful shutdown frame, kill on
// timeout, then runtime-specific cleanup.
func (i *Instance) Close() error {
	_ = wire.WriteFrame(i.conn, &wire.Frame{Type: wire.TypeShutdown, ID: "sd"})

	gracefulDone := make(chan struct{})
	go func() {
		_, _ = wire.ReadFrame(i.conn) // best-effort drain
		close(gracefulDone)
	}()
	select {
	case <-gracefulDone:
	case <-time.After(2 * time.Second):
	}

	if i.stop != nil {
		_ = i.stop()
	}
	if i.wait != nil {
		i.wait()
	}

	if cerr := i.conn.Close(); cerr != nil && !errors.Is(cerr, io.ErrClosedPipe) {
		_ = cerr
	}
	_ = i.listen.Close()
	return os.RemoveAll(i.sockDir)
}

// inheritedEnvKeys are the only host env vars passed to user functions
// by default. Anything else (secrets, cloud-credential helpers, gateway
// internals) is dropped. The caller can re-add specifics via env=[].
var inheritedEnvKeys = []string{
	"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TMPDIR",
}

func inheritedEnv() []string {
	out := make([]string, 0, len(inheritedEnvKeys))
	for _, k := range inheritedEnvKeys {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// makeSocket creates a tempdir + listening Unix socket inside it.
func makeSocket() (dir, sockPath string, ln net.Listener, err error) {
	dir, err = os.MkdirTemp("", "cfunc-sock-")
	if err != nil {
		return "", "", nil, err
	}
	sockPath = filepath.Join(dir, "s.sock")
	ln, err = net.Listen("unix", sockPath)
	if err != nil {
		os.RemoveAll(dir)
		return "", "", nil, err
	}
	return dir, sockPath, ln, nil
}

// acceptOrKill waits for one accept; on timeout it invokes onTimeout
// (typically to kill the spawnee), unblocks the inner Accept goroutine
// by closing the listener, and drains any conn that raced in.
func acceptOrKill(ln net.Listener, timeout time.Duration, onTimeout func()) (net.Conn, error) {
	connCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		connCh <- c
	}()
	select {
	case c := <-connCh:
		return c, nil
	case err := <-errCh:
		onTimeout()
		return nil, fmt.Errorf("spawn: accept: %w", err)
	case <-time.After(timeout):
		onTimeout()
		// Close the listener so the Accept goroutine unblocks and we
		// don't leak a fd waiting for an accept that won't come.
		_ = ln.Close()
		select {
		case c := <-connCh:
			c.Close()
		case <-errCh:
		case <-time.After(100 * time.Millisecond):
		}
		return nil, errors.New("spawn: timeout waiting for user process to dial")
	}
}

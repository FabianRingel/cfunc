// Package runc wraps the runc binary as a thin OCI-runtime client. It is
// intentionally minimal: create a bundle dir with config.json, call
// `runc run`, signal/kill, and clean up. Anything more sophisticated
// (state transitions, events, exec) we add when the use case appears.
//
// The package compiles on any OS but only functions where the runc
// binary exists; tests that actually invoke runc are gated behind a
// build tag.
package runc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
)

// Bundle is a directory containing config.json plus a rootfs.
// It is the on-disk artifact that runc consumes.
type Bundle struct {
	Dir        string // bundle directory
	RootfsPath string // path to rootfs (typically Dir/rootfs)
}

// WriteBundle creates dir, writes config.json, and ensures the rootfs
// path referenced by the spec exists. The rootfs itself must be
// populated by the caller (or pre-staged).
func WriteBundle(dir string, spec *rspec.Spec) (*Bundle, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(dir, "config.json")
	f, err := os.Create(cfgPath)
	if err != nil {
		return nil, err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(spec); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return &Bundle{Dir: dir, RootfsPath: spec.Root.Path}, nil
}

// Runtime invokes the runc binary on a Bundle.
type Runtime struct {
	// Binary overrides the runc path. Empty -> "runc" in PATH.
	Binary string
	// Sudo, if true, prefixes runc invocations with `sudo -n`. Required when
	// the calling process lacks the privileges/caps to set up the namespaces
	// the cfunc spec asks for (typical in dev VMs).
	Sudo bool
	// Stdout/Stderr receive the container's stdio. Nil -> os.Stdout/Stderr.
	Stdout io.Writer
	Stderr io.Writer
}

func (r *Runtime) command(ctx context.Context, args ...string) *exec.Cmd {
	bin := r.Binary
	if bin == "" {
		bin = "runc"
	}
	full := append([]string{bin}, args...)
	if r.Sudo {
		full = append([]string{"sudo", "-n"}, full...)
	}
	if ctx != nil {
		return exec.CommandContext(ctx, full[0], full[1:]...)
	}
	return exec.Command(full[0], full[1:]...)
}

// Run starts the container and blocks until it exits or ctx is cancelled.
// id must be unique per host.
func (r *Runtime) Run(ctx context.Context, id string, b *Bundle) error {
	cmd := r.command(ctx, "run", "--bundle", b.Dir, id)
	cmd.Stdout = orStdout(r.Stdout)
	cmd.Stderr = orStderr(r.Stderr)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Run()
}

// Kill sends signal to the container init process.
func (r *Runtime) Kill(id string, signal string) error {
	out, err := r.command(nil, "kill", id, signal).CombinedOutput()
	if err != nil {
		return fmt.Errorf("runc kill %s: %w: %s", id, err, out)
	}
	return nil
}

// Delete removes the container state. Safe to call after exit.
func (r *Runtime) Delete(id string) error {
	out, err := r.command(nil, "delete", "--force", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("runc delete %s: %w: %s", id, err, out)
	}
	return nil
}

// Available reports whether the runc binary is invokable in this
// environment. Useful for skipping integration tests on non-Linux dev
// machines.
func Available() bool {
	_, err := exec.LookPath("runc")
	return err == nil
}

// WaitFile polls for path to exist, up to timeout. Used after WriteBundle
// to confirm the rootfs is staged before runc fires.
func WaitFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("runc: %s did not appear within %s", path, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func orStdout(w io.Writer) io.Writer {
	if w == nil {
		return os.Stdout
	}
	return w
}
func orStderr(w io.Writer) io.Writer {
	if w == nil {
		return os.Stderr
	}
	return w
}

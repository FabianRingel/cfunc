package spawn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fabianringel/cfunc/internal/oci"
	"github.com/fabianringel/cfunc/internal/runc"
)

// RuncOptions controls the runc-backed spawner.
type RuncOptions struct {
	// RootfsBase is a host directory used as the read-only root for every
	// function container. It only needs to contain the standard mountpoint
	// stubs (/proc, /sys, /tmp, /run/cfunc, /cfunc); everything else comes
	// in via bind-mounts. If empty, EnsureBaseRootfs is called on a
	// per-process tempdir.
	RootfsBase string

	// RuncBinary overrides the runc executable path.
	RuncBinary string

	// Sudo prefixes runc invocations with `sudo -n` (rootless dev VMs).
	Sudo bool

	// ExtraLayers are mounted read-only into every container — used for
	// shared dependency layers in later phases.
	ExtraLayers []oci.Layer

	// DialTimeout is how long we wait for the user process inside the
	// container to dial back on the unix socket.
	DialTimeout time.Duration
}

// StartRunc launches binary inside an OCI container via runc.
//
// The host directory containing binary is bind-mounted read-only at
// /cfunc inside the container, and binary is invoked as
// /cfunc/<basename>. The function's socket directory is bind-mounted
// read-write at /run/cfunc.
func StartRunc(binary string, env []string, opts RuncOptions) (*Instance, error) {
	t0 := time.Now()
	if !filepath.IsAbs(binary) {
		abs, err := filepath.Abs(binary)
		if err != nil {
			return nil, err
		}
		binary = abs
	}
	st, err := os.Stat(binary)
	if err != nil {
		return nil, fmt.Errorf("spawn/runc: stat binary: %w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("spawn/runc: binary is a directory: %s", binary)
	}

	rootfs := opts.RootfsBase
	if rootfs == "" {
		var err error
		rootfs, err = EnsureBaseRootfs("")
		if err != nil {
			return nil, err
		}
	}

	bundleDir, err := os.MkdirTemp("", "cfunc-bundle-")
	if err != nil {
		return nil, err
	}

	sockDir, _, ln, err := makeSocket()
	if err != nil {
		os.RemoveAll(bundleDir)
		return nil, err
	}

	binDir := filepath.Dir(binary)
	binBase := filepath.Base(binary)
	containerBin := "/cfunc/" + binBase

	layers := append([]oci.Layer{}, opts.ExtraLayers...)
	layers = append(layers, oci.Layer{
		Name:      "user-binary",
		HostPath:  binDir,
		MountPath: "/cfunc",
	})

	spec, err := oci.Build(oci.Config{
		RootfsPath: rootfs,
		Binary:     containerBin,
		Env:        env,
		SocketDir:  sockDir,
		Layers:     layers,
	})
	if err != nil {
		ln.Close()
		os.RemoveAll(sockDir)
		os.RemoveAll(bundleDir)
		return nil, err
	}

	bundle, err := runc.WriteBundle(bundleDir, spec)
	if err != nil {
		ln.Close()
		os.RemoveAll(sockDir)
		os.RemoveAll(bundleDir)
		return nil, err
	}

	rt := &runc.Runtime{Binary: opts.RuncBinary, Sudo: opts.Sudo}
	id := "cfunc-" + randHex(6)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- rt.Run(ctx, id, bundle) }()

	dialTimeout := opts.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 10 * time.Second
	}
	conn, err := acceptOrKill(ln, dialTimeout, func() {
		cancel()
		_ = rt.Kill(id, "KILL")
	})
	if err != nil {
		ln.Close()
		_ = rt.Delete(id)
		os.RemoveAll(sockDir)
		os.RemoveAll(bundleDir)
		return nil, err
	}

	return &Instance{
		conn: conn, listen: ln, sockDir: sockDir,
		Mode:              "runc",
		ColdStartDuration: time.Since(t0),
		stop: func() error {
			err := rt.Kill(id, "TERM")
			cancel()
			return err
		},
		wait: func() {
			select {
			case <-runErr:
			case <-time.After(3 * time.Second):
				_ = rt.Kill(id, "KILL")
				<-runErr
			}
			_ = rt.Delete(id)
			os.RemoveAll(bundleDir)
		},
	}, nil
}

// EnsureBaseRootfs prepares a minimal rootfs at dir (created if "").
// The rootfs is empty apart from the standard mountpoint stubs that the
// cfunc OCI spec expects to bind into. Statically-linked Go user binaries
// require no further content. Idempotent.
func EnsureBaseRootfs(dir string) (string, error) {
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "cfunc-rootfs-")
		if err != nil {
			return "", err
		}
	}
	for _, sub := range []string{"proc", "sys", "dev", "tmp", "run/cfunc", "cfunc"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

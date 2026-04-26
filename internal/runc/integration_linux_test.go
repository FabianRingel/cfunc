//go:build linux && runc_integration

package runc_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fabianringel/cfunc/internal/oci"
	cruncc "github.com/fabianringel/cfunc/internal/runc"
)

// TestRunHelloWorld stages a minimal busybox rootfs, asks runc to execute
// /bin/echo inside it, and asserts the container's stdout reaches us.
//
// Run inside the Lima VM:
//
//	./scripts/lima-setup.sh test-runc
func TestRunHelloWorld(t *testing.T) {
	if !cruncc.Available() {
		t.Skip("runc not installed")
	}
	bb, err := exec.LookPath("busybox")
	if err != nil {
		t.Skip("busybox not installed: " + err.Error())
	}

	bundle := t.TempDir()
	rootfs := filepath.Join(bundle, "rootfs")
	stageBusybox(t, bb, rootfs)

	sockDir := t.TempDir()
	spec, err := oci.Build(oci.Config{
		RootfsPath: rootfs,
		Binary:     "/bin/echo",
		Args:       []string{"hello-from-cfunc"},
		SocketDir:  sockDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := cruncc.WriteBundle(bundle, spec)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	rt := &cruncc.Runtime{Stdout: &stdout, Stderr: &stderr, Sudo: os.Geteuid() != 0}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	id := "cfunc-test-" + randHex(6)
	defer rt.Delete(id)

	if err := rt.Run(ctx, id, b); err != nil {
		t.Fatalf("runc run: %v\nstderr: %s", err, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("hello-from-cfunc")) {
		t.Fatalf("unexpected stdout: %q (stderr=%q)", stdout.String(), stderr.String())
	}
}

// stageBusybox builds a minimal rootfs at dir using a single busybox
// binary plus the symlinks it needs (sh, echo).
func stageBusybox(t *testing.T, bb, dir string) {
	t.Helper()
	for _, sub := range []string{"bin", "proc", "sys", "dev", "tmp", "run/cfunc"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dir, "bin", "busybox")
	if err := copyFile(bb, target, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sh", "echo", "ls", "cat"} {
		_ = os.Symlink("busybox", filepath.Join(dir, "bin", name))
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 64*1024)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err.Error() == "EOF" {
				return nil
			}
			return err
		}
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

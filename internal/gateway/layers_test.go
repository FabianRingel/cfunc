// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/fabianringel/cfunc/internal/layerstore"
)

// memStore is an in-memory layerstore.Store for tests.
type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (m *memStore) Has(_ context.Context, digest string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[digest]
	return ok, nil
}

func (m *memStore) Get(_ context.Context, digest string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[digest]
	if !ok {
		return nil, layerstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memStore) Put(_ context.Context, digest string, r io.Reader, _ int64) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.data[digest] = b
	m.mu.Unlock()
	return nil
}

// makeTarGz returns a gzipped tar with one file at "hello/world.txt"
// containing the given body, plus the sha256 of the gzipped bytes.
func makeTarGz(t *testing.T, body string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "hello/world.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	h := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), "sha256:" + hex.EncodeToString(h[:])
}

func TestStoreResolverPullsAndExtracts(t *testing.T) {
	cache := t.TempDir()
	store := newMemStore()
	body, digest := makeTarGz(t, "hi")
	if err := store.Put(context.Background(), digest, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}

	r := NewStoreResolver(store, cache)
	mounts := []LayerMount{{Name: "L", MountPath: "/opt/L", Digest: digest}}
	if err := r.Resolve(context.Background(), mounts); err != nil {
		t.Fatal(err)
	}
	if mounts[0].HostPath == "" {
		t.Fatal("HostPath not set")
	}
	got, err := os.ReadFile(filepath.Join(mounts[0].HostPath, "hello", "world.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Fatalf("got %q", got)
	}

	// Second resolve hits the cache — no fetch even if we wipe the
	// store.
	store.mu.Lock()
	delete(store.data, digest)
	store.mu.Unlock()
	mounts2 := []LayerMount{{Name: "L", MountPath: "/opt/L", Digest: digest}}
	if err := r.Resolve(context.Background(), mounts2); err != nil {
		t.Fatalf("cached resolve: %v", err)
	}
}

func TestStoreResolverDigestMismatchRejected(t *testing.T) {
	cache := t.TempDir()
	store := newMemStore()
	body, _ := makeTarGz(t, "evil")
	wrongDigest := "sha256:" + hex.EncodeToString(make([]byte, 32))
	_ = store.Put(context.Background(), wrongDigest, bytes.NewReader(body), int64(len(body)))

	r := NewStoreResolver(store, cache)
	err := r.Resolve(context.Background(),
		[]LayerMount{{Name: "L", MountPath: "/opt/L", Digest: wrongDigest}})
	if err == nil {
		t.Fatal("expected digest-mismatch error")
	}
}

func TestNopResolverRejectsMissingPath(t *testing.T) {
	err := NopResolver{}.Resolve(context.Background(),
		[]LayerMount{{Name: "L", HostPath: "/does/not/exist", MountPath: "/opt/L"}})
	if err == nil {
		t.Fatal("expected error for missing host path")
	}
	if !errors.Is(err, os.ErrNotExist) && !errIsStat(err) {
		// loose check: any non-nil error is acceptable
	}
}

func errIsStat(err error) bool {
	var pathErr *os.PathError
	return errors.As(err, &pathErr)
}

// makeMaliciousTarGz returns a gzipped tar containing a symlink that
// escapes the root, plus a regular-file entry that tries to write
// through it. Used to verify the tar-slip defences.
func makeMaliciousTarGz(t *testing.T) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// 1) symlink "evil" -> "../../../../../../tmp"  (escapes root)
	_ = tw.WriteHeader(&tar.Header{
		Name:     "evil",
		Linkname: "../../../../../../tmp",
		Typeflag: tar.TypeSymlink,
	})
	// 2) regular file "evil/pwn" — would be written through the symlink
	body := []byte("pwned")
	_ = tw.WriteHeader(&tar.Header{
		Name: "evil/pwn", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write(body)
	tw.Close()
	gz.Close()
	h := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), "sha256:" + hex.EncodeToString(h[:])
}

func TestStoreResolverRejectsTarSlipSymlink(t *testing.T) {
	cache := t.TempDir()
	store := newMemStore()
	body, digest := makeMaliciousTarGz(t)
	_ = store.Put(context.Background(), digest, bytes.NewReader(body), int64(len(body)))

	r := NewStoreResolver(store, cache)
	err := r.Resolve(context.Background(),
		[]LayerMount{{Name: "L", MountPath: "/opt/L", Digest: digest}})
	if err == nil {
		t.Fatal("expected tar-slip rejection")
	}
	// Confirm nothing leaked to /tmp/pwn.
	if _, err := os.Stat("/tmp/pwn"); err == nil {
		t.Fatal("symlink was followed — /tmp/pwn was created")
	}
}

func TestStoreResolverRejectsAbsoluteSymlink(t *testing.T) {
	cache := t.TempDir()
	store := newMemStore()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{
		Name: "link", Linkname: "/etc", Typeflag: tar.TypeSymlink,
	})
	tw.Close()
	gz.Close()
	h := sha256.Sum256(buf.Bytes())
	digest := "sha256:" + hex.EncodeToString(h[:])
	_ = store.Put(context.Background(), digest, bytes.NewReader(buf.Bytes()), int64(buf.Len()))

	r := NewStoreResolver(store, cache)
	err := r.Resolve(context.Background(),
		[]LayerMount{{Name: "L", MountPath: "/opt/L", Digest: digest}})
	if err == nil {
		t.Fatal("expected absolute-target rejection")
	}
}

func TestStoreResolverRejectsDotDotPath(t *testing.T) {
	cache := t.TempDir()
	store := newMemStore()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{
		Name: "../escape.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	h := sha256.Sum256(buf.Bytes())
	digest := "sha256:" + hex.EncodeToString(h[:])
	_ = store.Put(context.Background(), digest, bytes.NewReader(buf.Bytes()), int64(buf.Len()))

	r := NewStoreResolver(store, cache)
	err := r.Resolve(context.Background(),
		[]LayerMount{{Name: "L", MountPath: "/opt/L", Digest: digest}})
	if err == nil {
		t.Fatal("dot-dot path should have been rejected with an error")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cache), "escape.txt")); err == nil {
		t.Fatal("dot-dot path escaped extraction root")
	}
}

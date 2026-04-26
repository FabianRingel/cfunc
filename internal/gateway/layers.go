// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fabianringel/cfunc/internal/layerstore"
)

// LayerResolver materialises layers on the local host before a function
// is spawned. Implementations:
//
//   - NopResolver:  errors only if a layer with Digest set is missing
//                   on disk and no store is available to fetch it.
//   - StoreResolver: pulls missing layers from a layerstore.Store
//                   (Hetzner Object Storage, RustFS, …) by digest.
//
// Resolve is called per-acquire; impls must be safe for concurrent use
// and idempotent (multiple goroutines may resolve the same digest).
type LayerResolver interface {
	Resolve(ctx context.Context, mounts []LayerMount) error
}

// NopResolver is the default. It accepts mounts whose HostPath already
// exists and rejects mounts whose HostPath is missing — there's no
// store to pull from.
type NopResolver struct{}

func (NopResolver) Resolve(_ context.Context, mounts []LayerMount) error {
	for _, m := range mounts {
		if m.HostPath == "" {
			return fmt.Errorf("layer %q: empty HostPath and no resolver", m.Name)
		}
		if _, err := os.Stat(m.HostPath); err != nil {
			return fmt.Errorf("layer %q: %w", m.Name, err)
		}
	}
	return nil
}

// StoreResolver pulls missing layers from a layerstore.Store and
// extracts them under a host cache root (one directory per digest).
// Mounts without Digest are validated like NopResolver.
type StoreResolver struct {
	Store    layerstore.Store
	CacheDir string

	mu      sync.Mutex
	pulling map[string]chan struct{} // digest -> done channel; in-flight pulls
}

// NewStoreResolver constructs a StoreResolver. CacheDir must exist;
// it's the host directory beneath which per-digest layer trees are
// materialised. The resolver rewrites m.HostPath to point at the
// resolved tree.
func NewStoreResolver(store layerstore.Store, cacheDir string) *StoreResolver {
	return &StoreResolver{
		Store:    store,
		CacheDir: cacheDir,
		pulling:  map[string]chan struct{}{},
	}
}

func (r *StoreResolver) Resolve(ctx context.Context, mounts []LayerMount) error {
	for i := range mounts {
		m := &mounts[i]
		if m.Digest == "" {
			// No digest → fall back to NopResolver semantics.
			if m.HostPath == "" {
				return fmt.Errorf("layer %q: no Digest and no HostPath", m.Name)
			}
			if _, err := os.Stat(m.HostPath); err != nil {
				return fmt.Errorf("layer %q: %w", m.Name, err)
			}
			continue
		}
		path, err := r.ensureLocal(ctx, m.Digest)
		if err != nil {
			return fmt.Errorf("layer %q (%s): %w", m.Name, m.Digest, err)
		}
		m.HostPath = path
	}
	return nil
}

// ensureLocal returns the host path for digest, pulling and extracting
// if not already present. Concurrent callers for the same digest wait
// on a single in-flight pull instead of duplicating the download.
func (r *StoreResolver) ensureLocal(ctx context.Context, digest string) (string, error) {
	target := filepath.Join(r.CacheDir, sanitiseDigest(digest))
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}

	r.mu.Lock()
	if ch, ok := r.pulling[digest]; ok {
		r.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return target, nil
	}
	done := make(chan struct{})
	r.pulling[digest] = done
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pulling, digest)
		r.mu.Unlock()
		close(done)
	}()

	// Re-check after taking the slot — another goroutine may have
	// finished the pull while we were waiting on the lock.
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}

	rc, err := r.Store.Get(ctx, digest)
	if err != nil {
		return "", err
	}
	defer rc.Close()

	tmp, err := os.MkdirTemp(r.CacheDir, ".pulling-*")
	if err != nil {
		return "", err
	}
	cleanup := tmp
	defer func() {
		if cleanup != "" {
			_ = os.RemoveAll(cleanup)
		}
	}()
	gotDigest, err := extractTarGz(rc, tmp)
	if err != nil {
		return "", err
	}
	if !digestMatches(digest, gotDigest) {
		return "", fmt.Errorf("digest mismatch: declared %s, got %s", digest, gotDigest)
	}
	if err := os.Rename(tmp, target); err != nil {
		// Another goroutine likely won the race; if target now exists
		// that's fine.
		if _, statErr := os.Stat(target); statErr == nil {
			return target, nil
		}
		return "", err
	}
	cleanup = "" // success — don't remove
	return target, nil
}

// extractTarGz reads a gzipped tar from rc into dst and returns the
// sha256 of the *raw gzipped bytes* (the layer's content address).
func extractTarGz(rc io.Reader, dst string) (string, error) {
	h := sha256.New()
	tee := io.TeeReader(rc, h)
	gz, err := gzip.NewReader(tee)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if err := writeEntry(dst, hdr, tr); err != nil {
			return "", err
		}
	}
	// Drain any trailing bytes so the hash captures the full stream.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return "", err
	}
	return "sha256:" + hexHash(h), nil
}

func writeEntry(dst string, hdr *tar.Header, r io.Reader) error {
	target, err := safeJoin(dst, hdr.Name)
	if err != nil {
		return fmt.Errorf("unsafe tar entry %q: %w", hdr.Name, err)
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)
	case tar.TypeReg, tar.TypeRegA:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		// O_NOFOLLOW so a pre-existing symlink at target (planted by an
		// earlier malicious entry) can't redirect the write outside dst.
		// O_EXCL would be safer still but the layer format permits
		// duplicate paths; favour interoperability with explicit
		// remove-then-create.
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		f, err := os.OpenFile(target,
			os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscallNoFollow,
			os.FileMode(hdr.Mode)&0o777)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, r)
		return err
	case tar.TypeSymlink:
		// Reject symlinks whose target escapes dst, so a follow-up
		// regular-file entry can't be written through the link to an
		// arbitrary path on the host. Both absolute targets and ones
		// that walk up out of the layer root are forbidden.
		if filepath.IsAbs(hdr.Linkname) {
			return fmt.Errorf("symlink %q has absolute target %q", hdr.Name, hdr.Linkname)
		}
		linkAbs := filepath.Clean(filepath.Join(filepath.Dir(target), hdr.Linkname))
		dstAbs := filepath.Clean(dst)
		if !strings.HasPrefix(linkAbs, dstAbs+string(filepath.Separator)) && linkAbs != dstAbs {
			return fmt.Errorf("symlink %q escapes layer root", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.Symlink(hdr.Linkname, target)
	default:
		// Skip device files, hardlinks, etc. — layers are filesystem trees.
		return nil
	}
}

// safeJoin returns a path inside dst constructed from name, rejecting
// any component that would escape via "..", absolute paths, or other
// path-traversal tricks. The result is filepath.Clean-ed.
func safeJoin(dst, name string) (string, error) {
	clean := filepath.Clean("/" + name) // root the path so .. can't escape
	if clean == "/" {
		return "", fmt.Errorf("empty name")
	}
	target := filepath.Join(dst, clean)
	dstAbs := filepath.Clean(dst)
	if !strings.HasPrefix(target, dstAbs+string(filepath.Separator)) && target != dstAbs {
		return "", fmt.Errorf("path escapes root")
	}
	return target, nil
}

func sanitiseDigest(d string) string {
	// "sha256:abc…" → "sha256-abc…" so it's a valid single path segment.
	return strings.ReplaceAll(d, ":", "-")
}

func digestMatches(declared, got string) bool {
	// Allow either "sha256:hex" or bare hex on either side.
	norm := func(s string) string {
		s = strings.TrimPrefix(s, "sha256:")
		return strings.ToLower(s)
	}
	return norm(declared) == norm(got)
}

func hexHash(h hash.Hash) string {
	return hex.EncodeToString(h.Sum(nil))
}

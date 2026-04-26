// Package layers implements the cfunc dependency-layer store.
//
// A layer is a host directory tree that gets bind-mounted read-only into
// every container that references it. Because identical layers reside at
// identical inodes on the host, the Linux page cache shares them across
// containers — which is the central memory-efficiency property of cfunc.
//
// Store layout under root:
//
//	<root>/
//	  blobs/<sha256>/        # content (bind-mount source)
//	  blobs/<sha256>.json    # manifest sidecar
//	  index.json             # name@version -> sha256
//
// Layers are content-addressed: the sha256 is computed over the file tree
// (sorted, with mode + size + content). Adding the same content twice is
// idempotent.
package layers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Manifest describes a single layer. Stored next to its content blob.
type Manifest struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Runtime   string `json:"runtime,omitempty"` // e.g. "go", "python-3.12", "any"
	MountPath string `json:"mount_path"`        // absolute path inside containers
	Digest    string `json:"digest"`            // sha256:...
	Size      int64  `json:"size_bytes"`
}

// Ref names a layer by name@version.
type Ref struct {
	Name    string
	Version string
}

func (r Ref) String() string { return r.Name + "@" + r.Version }

// ParseRef parses "name@version". Empty version is allowed and maps to
// the latest entry registered for that name.
func ParseRef(s string) (Ref, error) {
	if s == "" {
		return Ref{}, fmt.Errorf("layers: empty ref")
	}
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return Ref{Name: s}, nil
	}
	return Ref{Name: s[:at], Version: s[at+1:]}, nil
}

// Store persists layers under a single root directory.
type Store struct {
	root string
	mu   sync.Mutex
}

// Open opens (and creates if missing) a store rooted at root.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "blobs"), 0o755); err != nil {
		return nil, err
	}
	s := &Store{root: root}
	if _, err := os.Stat(s.indexPath()); os.IsNotExist(err) {
		if err := s.writeIndex(map[string]string{}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Root returns the on-disk root for this store.
func (s *Store) Root() string { return s.root }

// Add registers content from srcDir as layer name@version mounted at
// mountPath inside containers. Returns the resulting manifest.
//
// If a layer with identical content already exists, Add is idempotent and
// returns the existing manifest (the new name@version is still indexed).
func (s *Store) Add(name, version, mountPath, runtime, srcDir string) (*Manifest, error) {
	if name == "" || version == "" {
		return nil, fmt.Errorf("layers: name and version required")
	}
	if !filepath.IsAbs(mountPath) {
		return nil, fmt.Errorf("layers: mountPath must be absolute, got %q", mountPath)
	}
	srcDir, err := filepath.Abs(srcDir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(srcDir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("layers: srcDir is not a directory: %s", srcDir)
	}

	digest, size, err := hashTree(srcDir)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	blobDir := filepath.Join(s.root, "blobs", digest)
	if _, err := os.Stat(blobDir); os.IsNotExist(err) {
		tmp := blobDir + ".incoming"
		_ = os.RemoveAll(tmp)
		if err := copyTree(srcDir, tmp); err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		if err := os.Rename(tmp, blobDir); err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		// Drop write bits on regular files so accidental writes fail loudly.
		// Directories stay writable so the store itself can be cleaned up;
		// the read-only bind-mount inside containers prevents writes there.
		_ = filepath.Walk(blobDir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			_ = os.Chmod(p, info.Mode().Perm()&^0o222)
			return nil
		})
	}

	man := &Manifest{
		Name: name, Version: version, Runtime: runtime,
		MountPath: mountPath, Digest: "sha256:" + digest, Size: size,
	}
	if err := s.writeManifest(digest, man); err != nil {
		return nil, err
	}

	idx, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	idx[Ref{Name: name, Version: version}.String()] = digest
	if err := s.writeIndex(idx); err != nil {
		return nil, err
	}
	return man, nil
}

// Resolve returns the manifest and on-disk content path for ref.
// An empty Version selects the most recently added entry for that name.
func (s *Store) Resolve(ref Ref) (*Manifest, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.readIndex()
	if err != nil {
		return nil, "", err
	}
	digest := ""
	if ref.Version != "" {
		digest = idx[ref.String()]
	} else {
		// pick any entry matching name (deterministic by sort)
		var matches []string
		prefix := ref.Name + "@"
		for k := range idx {
			if strings.HasPrefix(k, prefix) {
				matches = append(matches, k)
			}
		}
		if len(matches) > 0 {
			sort.Strings(matches)
			digest = idx[matches[len(matches)-1]]
		}
	}
	if digest == "" {
		return nil, "", fmt.Errorf("layers: not found: %s", ref)
	}
	man, err := s.readManifest(digest)
	if err != nil {
		return nil, "", err
	}
	return man, filepath.Join(s.root, "blobs", digest), nil
}

// List returns all (ref, manifest) pairs in the index, sorted by ref.
func (s *Store) List() ([]Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	var refs []string
	for k := range idx {
		refs = append(refs, k)
	}
	sort.Strings(refs)
	out := make([]Manifest, 0, len(refs))
	for _, r := range refs {
		m, err := s.readManifest(idx[r])
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, nil
}

func (s *Store) indexPath() string { return filepath.Join(s.root, "index.json") }

func (s *Store) readIndex() (map[string]string, error) {
	b, err := os.ReadFile(s.indexPath())
	if err != nil {
		return nil, err
	}
	var idx map[string]string
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	if idx == nil {
		idx = map[string]string{}
	}
	return idx, nil
}

func (s *Store) writeIndex(idx map[string]string) error {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.indexPath(), b, 0o644)
}

func (s *Store) writeManifest(digest string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.root, "blobs", digest+".json"), b, 0o644)
}

func (s *Store) readManifest(digest string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "blobs", digest+".json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// hashTree computes a stable sha256 over the file tree rooted at dir,
// covering relative path, mode, size, and content. Returns hex digest +
// total content bytes.
func hashTree(dir string) (string, int64, error) {
	h := sha256.New()
	var total int64
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s\x00%o\x00%d\x00", rel, info.Mode().Perm(), info.Size())
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			n, err := io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			total += n
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		cerr := out.Close()
		if err != nil {
			return err
		}
		return cerr
	})
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

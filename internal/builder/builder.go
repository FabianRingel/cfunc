package builder

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// Builder runs build operations under a fixed Policy and Limits.
type Builder struct {
	Policy Policy
	Limits Limits
	// Tag identifies this builder in produced manifests (hostname, image, etc.).
	Tag string
}

// New constructs a Builder with sensible defaults filled in.
func New(p Policy, l Limits) *Builder {
	return &Builder{
		Policy: p,
		Limits: l.WithDefaults(),
	}
}

// Build validates the spec and dispatches to the configured engine.
// Returns a Result containing the manifest and a gzipped tar of the
// build output (base64 in the JSON envelope).
func (b *Builder) Build(ctx context.Context, spec BuildSpec) (*BuildResult, error) {
	if err := spec.Validate(b.Policy); err != nil {
		return nil, err
	}
	switch spec.Build.Type {
	case "python-pip":
		return b.buildPythonPip(ctx, spec)
	default:
		return nil, fmt.Errorf("builder: unsupported type %q", spec.Build.Type)
	}
}

func (b *Builder) buildPythonPip(ctx context.Context, spec BuildSpec) (*BuildResult, error) {
	ctx, cancel := context.WithTimeout(ctx, b.Limits.Timeout)
	defer cancel()

	work, err := os.MkdirTemp("", "cfunc-build-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	target := filepath.Join(work, "site")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, err
	}
	reqPath := filepath.Join(work, "requirements.txt")
	if err := os.WriteFile(reqPath, []byte(spec.Build.Requirements), 0o600); err != nil {
		return nil, err
	}

	pythonBin, err := resolvePython(spec.Build.Python)
	if err != nil {
		return nil, err
	}

	args := []string{
		"-m", "pip", "install",
		"--target", target,
		"--requirement", reqPath,
		"--disable-pip-version-check",
		"--no-compile",
		"--no-cache-dir",
		"--quiet",
	}
	if b.Policy.RequireHashes {
		args = append(args, "--require-hashes")
	}
	if spec.Build.IndexURL != "" {
		args = append(args, "--index-url", spec.Build.IndexURL)
	}

	cmd := exec.CommandContext(ctx, pythonBin, args...)
	// Sanitized env: subprocess sees nothing of the gateway's secrets.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + work,    // pip writes its caches here, not the user's
		"TMPDIR=" + work,
		"LANG=C.UTF-8",
	}
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pip install failed: %w\n%s", err, trim(stderr.String()))
	}

	// Capacity check before tar/hash work.
	size, err := dirSize(target, int64(b.Limits.MaxOutputMB)*1024*1024)
	if err != nil {
		return nil, err
	}

	digest, err := hashDir(target)
	if err != nil {
		return nil, err
	}
	tarball, err := tarGz(target)
	if err != nil {
		return nil, err
	}

	mountPath := spec.MountPath
	if mountPath == "" {
		mountPath = "/opt/layers/" + spec.Name
	}
	runtime := spec.Runtime
	if runtime == "" {
		runtime = "python-" + spec.Build.Python
	}

	return &BuildResult{
		Manifest: Manifest{
			Name:       spec.Name,
			Version:    spec.Version,
			Runtime:    runtime,
			MountPath:  mountPath,
			Digest:     "sha256:" + digest,
			SizeBytes:  size,
			BuiltAt:    time.Now().UTC(),
			BuilderTag: b.Tag,
			Spec:       spec,
		},
		TarGzBase64: base64.StdEncoding.EncodeToString(tarball),
	}, nil
}

// resolvePython picks the python interpreter binary for the requested
// version. Prefers explicit "pythonX.Y", falls back to "python3".
func resolvePython(version string) (string, error) {
	candidates := []string{"python" + version, "python3", "python"}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("builder: no python interpreter found (tried %v)", candidates)
}

// dirSize sums regular-file bytes under root. Errors if the running
// total exceeds maxBytes — protects against a build that produces
// unexpectedly huge output.
func dirSize(root string, maxBytes int64) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
			if total > maxBytes {
				return fmt.Errorf("builder: output exceeds %d bytes", maxBytes)
			}
		}
		return nil
	})
	return total, err
}

// hashDir computes a stable sha256 over the file tree (relative path,
// mode, size, content). Symlinks are rejected — same policy as the
// layer store.
func hashDir(root string) (string, error) {
	type entry struct {
		rel  string
		info os.FileInfo
		path string
	}
	var entries []entry
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("builder: symlink in build output: %s", rel)
		}
		entries = append(entries, entry{rel: rel, info: info, path: p})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%o\x00%d\x00", e.rel, e.info.Mode().Perm(), e.info.Size())
		if e.info.Mode().IsRegular() {
			f, err := os.Open(e.path)
			if err != nil {
				return "", err
			}
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return "", err
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// tarGz produces a deterministic gzipped tar of root. Modtimes are
// zeroed so the same hashDir produces the same bytes.
func tarGz(root string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Header.ModTime = time.Time{}
	tw := tar.NewWriter(gz)

	type entry struct {
		rel, path string
		info      os.FileInfo
	}
	var entries []entry
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		entries = append(entries, entry{rel: rel, path: p, info: info})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	for _, e := range entries {
		hdr, err := tar.FileInfoHeader(e.info, "")
		if err != nil {
			return nil, err
		}
		hdr.Name = e.rel
		hdr.ModTime = time.Time{}
		hdr.AccessTime = time.Time{}
		hdr.ChangeTime = time.Time{}
		hdr.Uid, hdr.Gid = 0, 0
		hdr.Uname, hdr.Gname = "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if e.info.Mode().IsRegular() {
			f, err := os.Open(e.path)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Extract writes a gzipped tar (as produced by tarGz) into dest.
// Used by the gateway when it ingests a builder response.
func Extract(tgz []byte, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// Path-traversal guard: header name must not escape dest.
		clean := filepath.Clean(hdr.Name)
		if filepath.IsAbs(clean) || clean == ".." || hasPrefix(clean, "../") {
			return fmt.Errorf("builder: tar entry escapes dest: %q", hdr.Name)
		}
		target := filepath.Join(dest, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC,
				os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		default:
			// Skip symlinks, devices, fifos: same policy as input.
		}
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func trim(s string) string {
	const max = 4 * 1024
	if len(s) > max {
		return s[:max] + "...(truncated)"
	}
	return s
}

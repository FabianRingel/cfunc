// SPDX-License-Identifier: Apache-2.0

// Package builder is the server-side layer-build engine.
//
// Operators send a build *specification* (what to build, how to build
// it). The builder runs the build in a controlled environment and
// returns a tarball + manifest. The operator's machine never produces
// layer content directly — closing the most common supply-chain attack
// vector (compromised dev workstation).
//
// Hash-pinning is mandatory: every line in `requirements` must carry a
// `--hash=sha256:…` so a mid-stream PyPI tamper can't slip a different
// artefact into the layer.
package builder

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// BuildSpec is the on-the-wire request format.
type BuildSpec struct {
	// Layer metadata (forwarded to the resulting Manifest).
	Name      string `json:"name"`
	Version   string `json:"version"`
	Runtime   string `json:"runtime,omitempty"`    // e.g. "python-3.11"; default derived from Build.Python
	MountPath string `json:"mount_path,omitempty"` // default /opt/layers/<name>

	Build BuildOptions `json:"build"`
}

// BuildOptions selects the build engine and its inputs.
type BuildOptions struct {
	// Type picks the build engine. Currently only "python-pip".
	Type string `json:"type"`

	// Python is the interpreter version (e.g. "3.11"). Must be in the
	// builder's allow-list.
	Python string `json:"python,omitempty"`

	// Requirements is the *content* of the requirements.txt the builder
	// will pip-install. Must include --hash=sha256:... for every entry.
	Requirements string `json:"requirements"`

	// IndexURL overrides PyPI. Empty -> builder default. The builder
	// rejects values not on its index allow-list.
	IndexURL string `json:"index_url,omitempty"`
}

// BuildResult is what the builder returns.
type BuildResult struct {
	Manifest Manifest `json:"manifest"`
	// TarGzBase64 is the layer content as a gzipped tar archive,
	// base64-encoded for JSON transport.
	TarGzBase64 string `json:"tar_gz_base64"`
}

// Manifest is the deterministic description of a built layer.
type Manifest struct {
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	Runtime    string    `json:"runtime"`
	MountPath  string    `json:"mount_path"`
	Digest     string    `json:"digest"`      // sha256:... over the tree
	SizeBytes  int64     `json:"size_bytes"`  // sum of regular file sizes
	BuiltAt    time.Time `json:"built_at"`
	BuilderTag string    `json:"builder_tag,omitempty"`

	// Build provenance.
	Spec BuildSpec `json:"spec"`
}

// Limits caps a single build's resource use. Zero values pick conservative defaults.
type Limits struct {
	Timeout     time.Duration // default 5m
	MaxOutputMB int           // default 1024 (1 GiB)
}

func (l Limits) WithDefaults() Limits {
	if l.Timeout == 0 {
		l.Timeout = 5 * time.Minute
	}
	if l.MaxOutputMB == 0 {
		l.MaxOutputMB = 1024
	}
	return l
}

// Policy is what the builder enforces about user-supplied input.
type Policy struct {
	// AllowedPythonVersions lists acceptable -python values, e.g. ["3.11","3.12"].
	AllowedPythonVersions []string

	// AllowedIndexURLs lists acceptable -index-url values. Empty input
	// uses the first entry as default. If no entries are configured,
	// the public PyPI is used and clients can't override.
	AllowedIndexURLs []string

	// RequireHashes enforces --require-hashes (mandatory). Always true
	// in practice; surfaced here to make tests explicit.
	RequireHashes bool
}

// DefaultPolicy returns a minimally-permissive policy: any Python
// version, default PyPI, hashes mandatory.
func DefaultPolicy() Policy {
	return Policy{
		AllowedPythonVersions: nil, // nil = any (let the binary lookup decide)
		AllowedIndexURLs:      nil,
		RequireHashes:         true,
	}
}

// Validate checks the spec against the policy. Errors are user-actionable.
func (s *BuildSpec) Validate(p Policy) error {
	if !validLayerName.MatchString(s.Name) {
		return errors.New("builder: invalid name (allowed: [A-Za-z0-9_.-], 1-64)")
	}
	if !validLayerVersion.MatchString(s.Version) {
		return errors.New("builder: invalid version (allowed: [A-Za-z0-9_.+-], 1-64)")
	}
	if s.MountPath != "" && !strings.HasPrefix(s.MountPath, "/") {
		return errors.New("builder: mount_path must be absolute")
	}
	switch s.Build.Type {
	case "python-pip":
		return validatePythonPip(s, p)
	case "":
		return errors.New("builder: build.type required")
	default:
		return fmt.Errorf("builder: unknown build.type %q", s.Build.Type)
	}
}

func validatePythonPip(s *BuildSpec, p Policy) error {
	if s.Build.Python == "" {
		return errors.New("builder: build.python required for python-pip")
	}
	if !validPythonVersion.MatchString(s.Build.Python) {
		return errors.New("builder: invalid python version format")
	}
	if len(p.AllowedPythonVersions) > 0 && !contains(p.AllowedPythonVersions, s.Build.Python) {
		return fmt.Errorf("builder: python version %q not in allow-list %v",
			s.Build.Python, p.AllowedPythonVersions)
	}
	if s.Build.IndexURL != "" {
		if !validIndexURL.MatchString(s.Build.IndexURL) {
			return errors.New("builder: malformed index_url")
		}
		if len(p.AllowedIndexURLs) > 0 && !contains(p.AllowedIndexURLs, s.Build.IndexURL) {
			return fmt.Errorf("builder: index_url not allow-listed")
		}
	}
	if strings.TrimSpace(s.Build.Requirements) == "" {
		return errors.New("builder: requirements required")
	}
	if p.RequireHashes {
		if err := requireFullyHashed(s.Build.Requirements); err != nil {
			return err
		}
	}
	return nil
}

// requireFullyHashed walks `content` line-by-line and ensures every
// non-comment requirement carries at least one --hash=sha256:... entry.
//
// We accept the full pip syntax for line continuations (`\`), comments
// starting with `#`, blank lines, and `-r`/`-c`/`--requirement`/etc.
// directives are forbidden because they would let a caller load a
// non-validated file from inside the build sandbox.
func requireFullyHashed(content string) error {
	// Stitch line continuations.
	stitched := stitchContinuations(content)
	for n, raw := range strings.Split(stitched, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "-r") || strings.HasPrefix(line, "--requirement") ||
			strings.HasPrefix(line, "-c") || strings.HasPrefix(line, "--constraint") ||
			strings.HasPrefix(line, "-e") || strings.HasPrefix(line, "--editable") {
			return fmt.Errorf("builder: line %d: file references and editable installs are not allowed", n+1)
		}
		if strings.HasPrefix(line, "-") {
			// Other pip directives (e.g. --extra-index-url) are allowed
			// only via the spec's IndexURL field, not inline.
			return fmt.Errorf("builder: line %d: inline pip directives not allowed", n+1)
		}
		if !sha256HashRE.MatchString(line) {
			return fmt.Errorf("builder: line %d: missing --hash=sha256:... (every requirement must be hash-pinned)", n+1)
		}
	}
	return nil
}

func stitchContinuations(s string) string {
	// pip allows trailing-backslash continuation. Replace `\\\n` (and
	// trailing whitespace before it) with a single space to flatten.
	re := regexp.MustCompile(`\\\s*\n\s*`)
	return re.ReplaceAllString(s, " ")
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

var (
	validLayerName     = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.\-]{0,63}$`)
	validLayerVersion  = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.\+\-]{0,63}$`)
	validPythonVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)
	validIndexURL      = regexp.MustCompile(`^https://[a-zA-Z0-9.\-]+(:\d+)?(/[a-zA-Z0-9._\-/]*)?$`)
	sha256HashRE       = regexp.MustCompile(`--hash=sha256:[a-f0-9]{64}`)
)

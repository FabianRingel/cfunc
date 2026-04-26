// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"strings"
	"testing"
)

const validHash = "--hash=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validSpec() BuildSpec {
	return BuildSpec{
		Name:    "pylib",
		Version: "1.0.0",
		Build: BuildOptions{
			Type:         "python-pip",
			Python:       "3.11",
			Requirements: "numpy==1.26.0 " + validHash,
		},
	}
}

func TestValidateAccepts(t *testing.T) {
	s := validSpec()
	if err := s.Validate(DefaultPolicy()); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

func TestValidateRejectsUnpinnedRequirement(t *testing.T) {
	s := validSpec()
	s.Build.Requirements = "numpy==1.26.0\n"
	if err := s.Validate(DefaultPolicy()); err == nil {
		t.Fatal("expected hash-pin error")
	}
}

func TestValidateRejectsMixedHashedAndUnhashed(t *testing.T) {
	s := validSpec()
	s.Build.Requirements = "numpy==1.26.0 " + validHash + "\nrequests==2.31.0"
	err := s.Validate(DefaultPolicy())
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected line-2 error, got %v", err)
	}
}

func TestValidateAcceptsLineContinuations(t *testing.T) {
	s := validSpec()
	s.Build.Requirements = "numpy==1.26.0 \\\n    " + validHash + "\n"
	if err := s.Validate(DefaultPolicy()); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

func TestValidateAcceptsCommentsAndBlanks(t *testing.T) {
	s := validSpec()
	s.Build.Requirements = "# header\n\nnumpy==1.26.0 " + validHash + "\n# trailing\n"
	if err := s.Validate(DefaultPolicy()); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

func TestValidateRejectsForbiddenDirectives(t *testing.T) {
	cases := []string{
		"-r other-reqs.txt",
		"--requirement secret.txt",
		"-c constraints.txt",
		"-e ./local-pkg",
		"--editable ./local-pkg",
		"--extra-index-url https://example.org/simple",
	}
	for _, line := range cases {
		s := validSpec()
		s.Build.Requirements = line + "\n"
		if err := s.Validate(DefaultPolicy()); err == nil {
			t.Errorf("expected reject for %q", line)
		}
	}
}

func TestValidateLayerName(t *testing.T) {
	bad := []string{"", "with/slash", "name with space", "../evil", strings.Repeat("a", 65), ".dot"}
	for _, n := range bad {
		s := validSpec()
		s.Name = n
		if err := s.Validate(DefaultPolicy()); err == nil {
			t.Errorf("expected reject for name %q", n)
		}
	}
}

func TestValidatePythonVersionPolicy(t *testing.T) {
	p := DefaultPolicy()
	p.AllowedPythonVersions = []string{"3.11", "3.12"}

	s := validSpec()
	s.Build.Python = "3.13"
	if err := s.Validate(p); err == nil {
		t.Fatal("expected python-version reject")
	}

	s.Build.Python = "3.11"
	if err := s.Validate(p); err != nil {
		t.Fatalf("3.11 should pass: %v", err)
	}
}

func TestValidateIndexURLPolicy(t *testing.T) {
	p := DefaultPolicy()
	p.AllowedIndexURLs = []string{"https://pypi.internal/simple"}

	s := validSpec()
	s.Build.IndexURL = "https://evil.example/simple"
	if err := s.Validate(p); err == nil {
		t.Fatal("expected index-url reject")
	}

	s.Build.IndexURL = "https://pypi.internal/simple"
	if err := s.Validate(p); err != nil {
		t.Fatalf("allow-listed url should pass: %v", err)
	}
}

func TestValidateRejectsMalformedIndexURL(t *testing.T) {
	s := validSpec()
	s.Build.IndexURL = "http://insecure-pypi/simple"
	if err := s.Validate(DefaultPolicy()); err == nil {
		t.Fatal("expected http rejection")
	}
}

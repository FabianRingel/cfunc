package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cfunc.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	p := writeManifest(t, `
name: hello
runtime: go
binary: ./hello
layers:
  - shared-config@1.0
  - numpy@1.26
`)
	f, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "hello" || f.Runtime != "go" {
		t.Fatalf("got %+v", f)
	}
	if len(f.Layers) != 2 {
		t.Fatalf("layers=%v", f.Layers)
	}
	// Binary resolved against manifest dir.
	if !filepath.IsAbs(f.Binary) {
		t.Fatalf("binary not absolute: %q", f.Binary)
	}
}

func TestLoadAbsoluteBinaryUntouched(t *testing.T) {
	p := writeManifest(t, `
name: x
runtime: go
binary: /usr/local/bin/myfn
`)
	f, _ := Load(p)
	if f.Binary != "/usr/local/bin/myfn" {
		t.Fatalf("got %q", f.Binary)
	}
}

func TestRejectsMissingFields(t *testing.T) {
	cases := []string{
		`runtime: go
binary: ./x`,
		`name: x
binary: ./x`,
		`name: x
runtime: go`,
	}
	for i, c := range cases {
		p := writeManifest(t, c)
		if _, err := Load(p); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestRejectsInvalidYAML(t *testing.T) {
	p := writeManifest(t, "not: : valid")
	if _, err := Load(p); err == nil {
		t.Fatal("expected parse error")
	}
}

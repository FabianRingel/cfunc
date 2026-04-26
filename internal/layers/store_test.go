package layers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeSrc(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAddAndResolve(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := makeSrc(t, map[string]string{
		"lib/foo.py": "print('hi')",
		"lib/bar.py": "x=1",
	})
	m, err := store.Add("numpy", "1.26.0", "/opt/layers/numpy", "python-3.12", src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m.Digest, "sha256:") {
		t.Fatalf("digest=%q", m.Digest)
	}
	if m.Size == 0 {
		t.Fatal("size=0")
	}

	got, path, err := store.Resolve(Ref{Name: "numpy", Version: "1.26.0"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != m.Digest {
		t.Fatalf("digest mismatch")
	}
	if _, err := os.Stat(filepath.Join(path, "lib", "foo.py")); err != nil {
		t.Fatalf("content missing: %v", err)
	}
}

func TestAddIsContentAddressedAndIdempotent(t *testing.T) {
	store, _ := Open(t.TempDir())
	src1 := makeSrc(t, map[string]string{"a.txt": "hello"})
	src2 := makeSrc(t, map[string]string{"a.txt": "hello"})

	m1, _ := store.Add("L", "1", "/m", "any", src1)
	m2, _ := store.Add("L", "2", "/m", "any", src2)
	if m1.Digest != m2.Digest {
		t.Fatalf("identical content but different digests: %s vs %s", m1.Digest, m2.Digest)
	}

	src3 := makeSrc(t, map[string]string{"a.txt": "different"})
	m3, _ := store.Add("L", "3", "/m", "any", src3)
	if m1.Digest == m3.Digest {
		t.Fatal("different content but same digest")
	}
}

func TestResolveLatestVersion(t *testing.T) {
	store, _ := Open(t.TempDir())
	store.Add("L", "1.0.0", "/m", "any", makeSrc(t, map[string]string{"a": "1"}))
	store.Add("L", "2.0.0", "/m", "any", makeSrc(t, map[string]string{"a": "2"}))
	got, _, err := store.Resolve(Ref{Name: "L"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "2.0.0" {
		t.Fatalf("expected latest 2.0.0, got %q", got.Version)
	}
}

func TestList(t *testing.T) {
	store, _ := Open(t.TempDir())
	store.Add("a", "1", "/m", "any", makeSrc(t, map[string]string{"x": "1"}))
	store.Add("b", "1", "/m", "any", makeSrc(t, map[string]string{"x": "2"}))
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d", len(list))
	}
	if list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("not sorted: %v", list)
	}
}

func TestResolveMissing(t *testing.T) {
	store, _ := Open(t.TempDir())
	if _, _, err := store.Resolve(Ref{Name: "nope", Version: "1"}); err == nil {
		t.Fatal("expected not found")
	}
}

func TestParseRef(t *testing.T) {
	r, err := ParseRef("foo@1.0")
	if err != nil || r.Name != "foo" || r.Version != "1.0" {
		t.Fatalf("got %+v err=%v", r, err)
	}
	r, _ = ParseRef("just-name")
	if r.Name != "just-name" || r.Version != "" {
		t.Fatalf("got %+v", r)
	}
	if _, err := ParseRef(""); err == nil {
		t.Fatal("expected error")
	}
}

func TestRejectsRelativeMount(t *testing.T) {
	store, _ := Open(t.TempDir())
	src := makeSrc(t, map[string]string{"a": "1"})
	if _, err := store.Add("L", "1", "rel/path", "any", src); err == nil {
		t.Fatal("expected error for relative mountPath")
	}
}

func TestRejectsSymlinksInSource(t *testing.T) {
	dir := t.TempDir()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink that, if preserved, would resolve to /etc/shadow inside
	// any container that mounts the layer at /opt/x.
	if err := os.Symlink("/etc/shadow", filepath.Join(src, "evil")); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("L", "1", "/m", "any", src); err == nil {
		t.Fatal("expected error for symlink in source")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	root := t.TempDir()
	s1, _ := Open(root)
	s1.Add("L", "1", "/m", "any", makeSrc(t, map[string]string{"a": "1"}))

	s2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s2.Resolve(Ref{Name: "L", Version: "1"}); err != nil {
		t.Fatalf("not persisted: %v", err)
	}
}

// SPDX-License-Identifier: Apache-2.0

package runc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fabianringel/cfunc/internal/oci"
)

func TestWriteBundleProducesValidConfig(t *testing.T) {
	dir := t.TempDir()
	spec, err := oci.Build(oci.Config{
		RootfsPath: filepath.Join(dir, "rootfs"),
		Binary:     "/cfunc/runtime",
		SocketDir:  "/tmp/cfunc-sock-x",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := WriteBundle(dir, spec)
	if err != nil {
		t.Fatal(err)
	}
	if b.Dir != dir {
		t.Fatalf("dir=%q", b.Dir)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("config.json not valid JSON: %v", err)
	}
	if parsed["ociVersion"] == nil {
		t.Fatal("missing ociVersion")
	}
}

func TestAvailableDoesNotPanic(t *testing.T) {
	_ = Available() // result depends on host; just ensure no panic
}

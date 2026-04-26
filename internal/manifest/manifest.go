// Package manifest defines the cfunc.yaml schema that ships next to a
// function's source code. It tells the gateway how to run the function
// and which dependency layers to mount.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Function is the on-disk shape of cfunc.yaml.
type Function struct {
	// Name uniquely identifies the function within a gateway.
	Name string `yaml:"name"`

	// Runtime selects the SDK contract: "go", "python-3.12", "node-20", ...
	Runtime string `yaml:"runtime"`

	// Binary is the path (relative to the manifest file) of the compiled
	// user binary for languages that produce one (Go), or the entrypoint
	// script for interpreted languages.
	Binary string `yaml:"binary"`

	// Layers references shared dependency layers by "name@version".
	// Empty version means "latest registered".
	Layers []string `yaml:"layers,omitempty"`
}

// Load reads and validates a cfunc.yaml at path.
func Load(path string) (*Function, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Function
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("manifest: parse %s: %w", path, err)
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("manifest: %s: %w", path, err)
	}
	// Resolve Binary relative to the manifest dir so callers can use it
	// directly as an absolute path.
	if !filepath.IsAbs(f.Binary) {
		f.Binary = filepath.Join(filepath.Dir(path), f.Binary)
	}
	return &f, nil
}

// Validate checks required fields. Layers are not resolved here — that
// happens at gateway start so missing layers fail loudly with a useful
// error.
func (f *Function) Validate() error {
	if f.Name == "" {
		return fmt.Errorf("name is required")
	}
	if f.Runtime == "" {
		return fmt.Errorf("runtime is required")
	}
	if f.Binary == "" {
		return fmt.Errorf("binary is required")
	}
	return nil
}

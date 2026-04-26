package oci

import (
	"encoding/json"
	"strings"
	"testing"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
)

func validCfg() Config {
	return Config{
		RootfsPath: "/var/lib/cfunc/rootfs/go",
		Binary:     "/cfunc/runtime",
		Args:       []string{"--mode=fn"},
		Env:        []string{"FOO=bar"},
		SocketDir:  "/tmp/cfunc-sock-abc",
		Layers: []Layer{
			{Name: "numpy-1.26", HostPath: "/var/lib/cfunc/layers/sha-aaa", MountPath: "/opt/layers/numpy"},
		},
	}
}

func TestBuildHappyPath(t *testing.T) {
	spec, err := Build(validCfg())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != rspec.Version {
		t.Fatalf("version=%q", spec.Version)
	}
	if spec.Root == nil || !spec.Root.Readonly {
		t.Fatal("root must be read-only")
	}
	if got := spec.Process.Args[0]; got != "/cfunc/runtime" {
		t.Fatalf("argv[0]=%q", got)
	}
	if !envHas(spec.Process.Env, "CFUNC_SOCKET=/run/cfunc/s.sock") {
		t.Fatalf("CFUNC_SOCKET missing: %v", spec.Process.Env)
	}
	if !envHas(spec.Process.Env, "FOO=bar") {
		t.Fatal("user env missing")
	}
}

func TestBuildLayerMount(t *testing.T) {
	spec, err := Build(validCfg())
	if err != nil {
		t.Fatal(err)
	}
	var found *rspec.Mount
	for i, m := range spec.Mounts {
		if m.Destination == "/opt/layers/numpy" {
			found = &spec.Mounts[i]
		}
	}
	if found == nil {
		t.Fatal("layer mount not found")
	}
	if found.Source != "/var/lib/cfunc/layers/sha-aaa" {
		t.Fatalf("source=%q", found.Source)
	}
	if !contains(found.Options, "ro") {
		t.Fatalf("layer must be read-only, got opts %v", found.Options)
	}
}

func TestBuildSocketMount(t *testing.T) {
	spec, _ := Build(validCfg())
	for _, m := range spec.Mounts {
		if m.Destination == "/run/cfunc" {
			if m.Source != "/tmp/cfunc-sock-abc" {
				t.Fatalf("socket source=%q", m.Source)
			}
			if !contains(m.Options, "rw") {
				t.Fatalf("socket dir must be rw, got %v", m.Options)
			}
			return
		}
	}
	t.Fatal("socket mount not found")
}

func TestBuildHasIsolationNamespaces(t *testing.T) {
	spec, _ := Build(validCfg())
	want := map[rspec.LinuxNamespaceType]bool{
		rspec.PIDNamespace: false, rspec.MountNamespace: false,
		rspec.NetworkNamespace: false, rspec.IPCNamespace: false,
		rspec.UTSNamespace: false,
	}
	for _, ns := range spec.Linux.Namespaces {
		want[ns.Type] = true
	}
	for ns, ok := range want {
		if !ok {
			t.Errorf("missing namespace: %s", ns)
		}
	}
}

func TestBuildJSONSerializable(t *testing.T) {
	spec, _ := Build(validCfg())
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"ociVersion"`) {
		t.Fatalf("missing ociVersion in JSON")
	}
}

func TestBuildRejectsRelativeBinary(t *testing.T) {
	c := validCfg()
	c.Binary = "runtime"
	if _, err := Build(c); err == nil {
		t.Fatal("expected error for relative binary")
	}
}

func TestBuildRejectsRelativeLayerPaths(t *testing.T) {
	c := validCfg()
	c.Layers = []Layer{{HostPath: "rel", MountPath: "/x"}}
	if _, err := Build(c); err == nil {
		t.Fatal("expected error for relative HostPath")
	}
}

func TestBuildRejectsMissingFields(t *testing.T) {
	cases := map[string]func(*Config){
		"no rootfs":    func(c *Config) { c.RootfsPath = "" },
		"no binary":    func(c *Config) { c.Binary = "" },
		"no socketdir": func(c *Config) { c.SocketDir = "" },
	}
	for name, mut := range cases {
		c := validCfg()
		mut(&c)
		if _, err := Build(c); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func envHas(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func contains(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}

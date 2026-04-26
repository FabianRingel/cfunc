// Package oci builds OCI runtime specs (config.json) for cfunc function
// containers. The output is consumed by runc (or any OCI runtime) and is
// designed for the cfunc isolation model:
//
//   - Read-only root filesystem.
//   - Layer directories from the host bind-mounted read-only at fixed paths
//     so the kernel page cache shares them across all functions that
//     reference the same layer.
//   - The gateway's socket directory bind-mounted read-write so the user
//     process can dial back.
//   - Standard Linux namespaces (pid, ipc, uts, mount, network) for
//     isolation. User namespaces are out of scope for now.
package oci

import (
	"fmt"
	"path/filepath"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
)

// Layer is a host directory mounted read-only into the container at MountPath.
// Multiple containers referencing the same HostPath share page cache.
type Layer struct {
	Name      string // for logging / diagnostics
	HostPath  string // absolute path on host (read-only source)
	MountPath string // absolute path inside container
}

// Config is the input for Build. Fields are required unless documented.
type Config struct {
	// RootfsPath is the path to the prepared rootfs directory (must exist).
	RootfsPath string

	// Binary is the path *inside* the container of the user/runtime entrypoint.
	Binary string

	// Args are passed to Binary (Binary itself becomes argv[0]).
	Args []string

	// Env is the container's environment. CFUNC_SOCKET is appended automatically.
	Env []string

	// SocketDir is the host directory containing the unix socket. Bind-mounted
	// read-write at SocketDirInContainer.
	SocketDir string

	// SocketDirInContainer is where SocketDir appears inside the container.
	// Defaults to "/run/cfunc" if empty.
	SocketDirInContainer string

	// SocketName is the basename of the socket file inside SocketDir.
	// Defaults to "s.sock".
	SocketName string

	// Layers are read-only host directories to share between containers.
	Layers []Layer

	// Hostname for the container. Defaults to "cfunc".
	Hostname string
}

// Build assembles a runtime spec from cfg. The returned struct is JSON-
// serializable to config.json.
func Build(cfg Config) (*rspec.Spec, error) {
	if cfg.RootfsPath == "" {
		return nil, fmt.Errorf("oci: RootfsPath required")
	}
	if cfg.Binary == "" {
		return nil, fmt.Errorf("oci: Binary required")
	}
	if !filepath.IsAbs(cfg.Binary) {
		return nil, fmt.Errorf("oci: Binary must be absolute, got %q", cfg.Binary)
	}
	if cfg.SocketDir == "" {
		return nil, fmt.Errorf("oci: SocketDir required")
	}
	if !filepath.IsAbs(cfg.SocketDir) {
		return nil, fmt.Errorf("oci: SocketDir must be absolute, got %q", cfg.SocketDir)
	}

	sockDirIn := cfg.SocketDirInContainer
	if sockDirIn == "" {
		sockDirIn = "/run/cfunc"
	}
	sockName := cfg.SocketName
	if sockName == "" {
		sockName = "s.sock"
	}
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = "cfunc"
	}

	for i, l := range cfg.Layers {
		if l.HostPath == "" || !filepath.IsAbs(l.HostPath) {
			return nil, fmt.Errorf("oci: layer[%d] HostPath must be absolute", i)
		}
		if l.MountPath == "" || !filepath.IsAbs(l.MountPath) {
			return nil, fmt.Errorf("oci: layer[%d] MountPath must be absolute", i)
		}
	}

	args := append([]string{cfg.Binary}, cfg.Args...)
	env := append([]string{}, cfg.Env...)
	env = append(env, "CFUNC_SOCKET="+filepath.Join(sockDirIn, sockName))

	mounts := defaultMounts()
	mounts = append(mounts, rspec.Mount{
		Destination: sockDirIn,
		Source:      cfg.SocketDir,
		Type:        "bind",
		Options:     []string{"rbind", "rw"},
	})
	for _, l := range cfg.Layers {
		mounts = append(mounts, rspec.Mount{
			Destination: l.MountPath,
			Source:      l.HostPath,
			Type:        "bind",
			Options:     []string{"rbind", "ro"},
		})
	}

	spec := &rspec.Spec{
		Version: rspec.Version,
		Process: &rspec.Process{
			Terminal: false,
			User:     rspec.User{UID: 0, GID: 0},
			Args:     args,
			Env:      env,
			Cwd:      "/",
			Capabilities: &rspec.LinuxCapabilities{
				Bounding:  defaultCaps(),
				Effective: defaultCaps(),
				Permitted: defaultCaps(),
			},
			NoNewPrivileges: true,
		},
		Root:     &rspec.Root{Path: cfg.RootfsPath, Readonly: true},
		Hostname: hostname,
		Mounts:   mounts,
		Linux: &rspec.Linux{
			Namespaces: []rspec.LinuxNamespace{
				{Type: rspec.PIDNamespace},
				{Type: rspec.IPCNamespace},
				{Type: rspec.UTSNamespace},
				{Type: rspec.MountNamespace},
				{Type: rspec.NetworkNamespace},
			},
			MaskedPaths: []string{
				"/proc/kcore", "/proc/keys", "/proc/latency_stats",
				"/proc/timer_list", "/proc/timer_stats", "/proc/sched_debug",
				"/sys/firmware",
			},
			ReadonlyPaths: []string{
				"/proc/asound", "/proc/bus", "/proc/fs",
				"/proc/irq", "/proc/sys", "/proc/sysrq-trigger",
			},
		},
	}
	return spec, nil
}

// defaultCaps mirrors the Docker/containerd default capability set —
// enough for a typical Linux process (file ops, exec, network) without
// granting kernel-management caps. This is intentionally permissive for
// Phase 2; we tighten in Phase 6 when multi-tenant security is on the
// table.
func defaultCaps() []string {
	return []string{
		"CAP_AUDIT_WRITE",
		"CAP_CHOWN",
		"CAP_DAC_OVERRIDE",
		"CAP_FOWNER",
		"CAP_FSETID",
		"CAP_KILL",
		"CAP_MKNOD",
		"CAP_NET_BIND_SERVICE",
		"CAP_NET_RAW",
		"CAP_SETFCAP",
		"CAP_SETGID",
		"CAP_SETPCAP",
		"CAP_SETUID",
		"CAP_SYS_CHROOT",
	}
}

func defaultMounts() []rspec.Mount {
	return []rspec.Mount{
		{Destination: "/proc", Type: "proc", Source: "proc",
			Options: []string{"nosuid", "noexec", "nodev"}},
		{Destination: "/dev", Type: "tmpfs", Source: "tmpfs",
			Options: []string{"nosuid", "strictatime", "mode=755", "size=65536k"}},
		{Destination: "/dev/pts", Type: "devpts", Source: "devpts",
			Options: []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"}},
		{Destination: "/sys", Type: "sysfs", Source: "sysfs",
			Options: []string{"nosuid", "noexec", "nodev", "ro"}},
		{Destination: "/tmp", Type: "tmpfs", Source: "tmpfs",
			Options: []string{"nosuid", "nodev", "mode=1777", "size=65536k"}},
	}
}

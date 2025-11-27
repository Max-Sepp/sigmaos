package gvisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	ocirspec "github.com/opencontainers/runtime-spec/specs-go"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sched/msched/proc/srv/binsrv"
)

type Config struct {
	Spec *ocirspec.Spec
}

func NewDefaultConfig(p *proc.Proc) *Config {
	binPn := binsrv.BinPath(p.GetVersionedProgram())
	return NewDefaultConfigBinPath(p, binPn)
}

func NewDefaultConfigBinPath(p *proc.Proc, binPn string) *Config {
	return &Config{
		Spec: &ocirspec.Spec{
			Version: "1.0.0",
			Process: &ocirspec.Process{
				User: ocirspec.User{
					UID: 0,
					GID: 0,
				},
				Args: append([]string{binPn}, p.GetArgs()...),
				Env: []string{
					"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
					"TERM=xterm",
				},
				Cwd: "/",
				Capabilities: &ocirspec.LinuxCapabilities{
					Bounding: []string{
						"CAP_AUDIT_WRITE",
						"CAP_KILL",
						"CAP_NET_BIND_SERVICE",
					},
					Effective: []string{
						"CAP_AUDIT_WRITE",
						"CAP_KILL",
						"CAP_NET_BIND_SERVICE",
					},
					Inheritable: []string{
						"CAP_AUDIT_WRITE",
						"CAP_KILL",
						"CAP_NET_BIND_SERVICE",
					},
					Permitted: []string{
						"CAP_AUDIT_WRITE",
						"CAP_KILL",
						"CAP_NET_BIND_SERVICE",
					},
				},
				Rlimits: []ocirspec.POSIXRlimit{
					{
						Type: "RLIMIT_NOFILE",
						Hard: 1024,
						Soft: 1024,
					},
				},
			},
			Root: &ocirspec.Root{
				Path:     "/",
				Readonly: true,
			},
			Hostname: "runsc",
			Mounts: []ocirspec.Mount{
				{
					Destination: "/proc",
					Type:        "proc",
					Source:      "proc",
				},
				{
					Destination: "/dev",
					Type:        "tmpfs",
					Source:      "tmpfs",
				},
				{
					Destination: "/sys",
					Type:        "sysfs",
					Source:      "sysfs",
					Options: []string{
						"nosuid",
						"noexec",
						"nodev",
						"ro",
					},
				},
			},
			Linux: &ocirspec.Linux{
				Namespaces: []ocirspec.LinuxNamespace{
					{Type: ocirspec.PIDNamespace},
					{Type: ocirspec.IPCNamespace},
					{Type: ocirspec.UTSNamespace},
					{Type: ocirspec.MountNamespace},
				},
			},
		},
	}
}

func (c *Config) AddUserProcMounts() {
	c.Spec.Mounts = append(c.Spec.Mounts, []ocirspec.Mount{
		// Add mounts to make binfs available to the gvisor container
		{
			Destination: "/mnt",
			Type:        "bind",
			Source:      "mnt",
			Options: []string{
				"ro",
			},
		},
		{
			Destination: "/mnt/binfs",
			Type:        "bind",
			Source:      "mnt/bnfs",
			Options: []string{
				"ro",
			},
		},
		// Add mounts to make the sigmaos-perf directory available to the proc.
		// This dir is used to exfiltrate performance results
		{
			Destination: "/tmp",
			Type:        "bind",
			Source:      "tmp",
			Options: []string{
				"ro",
			},
		},
		{
			Destination: "/tmp/sigmaos-perf",
			Type:        "bind",
			Source:      "/tmp/sigmaos-perf",
		},
	}...)
}

func (c *Config) String() string {
	b, err := json.Marshal(c.Spec)
	if err != nil {
		db.DFatalf("Can't marshal OCI spec: %v", err)
	}
	return fmt.Sprintf("&{Spec: %s}", string(b))
}

func (c *Config) WriteToFile(bundleDirPathName string) error {
	b, err := json.MarshalIndent(c.Spec, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal spec: %w", err)
	}

	err = os.WriteFile(filepath.Join(bundleDirPathName, "config.json"), b, 0644)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

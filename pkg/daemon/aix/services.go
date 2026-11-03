package aix

import (
	"fmt"
	"strings"
)

// SRC subsystem names used for the node services the daemon manages. They are
// the names the AIX node stack (containerd-aix, the kubelet port and this
// daemon) registers with mkssys; see docs/aix.md.
const (
	SubsystemContainerd = "containerd"
	SubsystemKubelet    = "kubelet"
	SubsystemMCD        = "mcd"
	SubsystemGroup      = "kubernetes"
)

// unitAliases maps systemd unit base names that appear in MachineConfig node
// disruption policies and in the daemon itself to their AIX SRC subsystem.
// CRI-O's role is played by containerd-aix on AIX nodes.
var unitAliases = map[string]string{
	"crio": SubsystemContainerd,
}

// noReload lists subsystems that cannot re-read their configuration in place;
// a reload of these is performed as a restart.
var noReload = map[string]bool{
	SubsystemContainerd: true, // containerd has no config reload; running containers survive a restart (shims)
	SubsystemKubelet:    true,
}

// SubsystemForUnit translates a systemd unit name (for example "crio.service"
// or "kubelet") to the SRC subsystem that provides that service on AIX.
// Unit types that have no SRC equivalent (targets, sockets, timers, mounts,
// paths) are rejected.
func SubsystemForUnit(unit string) (string, error) {
	base := unit
	if i := strings.LastIndex(unit, "."); i > 0 {
		suffix := unit[i+1:]
		if suffix != "service" {
			return "", fmt.Errorf("systemd %s unit %q has no SRC equivalent on AIX", suffix, unit)
		}
		base = unit[:i]
	}
	if alias, ok := unitAliases[base]; ok {
		base = alias
	}
	if err := ValidateSubsystemName(base); err != nil {
		return "", fmt.Errorf("unit %q: %w", unit, err)
	}
	return base, nil
}

// ReloadMeansRestart reports whether reloading subsystem must be done as a restart.
func ReloadMeansRestart(subsystem string) bool {
	return noReload[subsystem]
}

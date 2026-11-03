package aix

import (
	"errors"
	"fmt"
	"strings"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

// ErrUnsupported marks MachineConfig content that has no AIX implementation.
var ErrUnsupported = errors.New("not supported on AIX")

// ValidateMachineConfig checks that a rendered MachineConfig only asks for
// things the daemon can do on an AIX node: write files, manage the core user's
// SSH keys and password, and run SRC-backed service actions. Everything that
// relies on RHCOS (rpm-ostree, the kernel command line, systemd, extensions,
// the realtime/64k kernels, FIPS mode) is rejected with one combined error so
// the node reports exactly what to remove from the AIX pool.
//
// Spec.OSImageURL is deliberately not checked: the render controller stamps the
// cluster's RHCOS image on every rendered config, and AIX nodes ignore it.
func ValidateMachineConfig(mc *mcfgv1.MachineConfig, ign *ign3types.Config) error {
	var errs []error
	unsupported := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %w", fmt.Sprintf(format, a...), ErrUnsupported))
	}

	if len(mc.Spec.KernelArguments) > 0 {
		unsupported("kernelArguments %v (tune AIX with chdev/vmo/no/ioo instead)", mc.Spec.KernelArguments)
	}
	if len(mc.Spec.Extensions) > 0 {
		unsupported("extensions %v (RHCOS extensions are RPM layers)", mc.Spec.Extensions)
	}
	if kt := strings.TrimSpace(mc.Spec.KernelType); kt != "" && kt != "default" {
		unsupported("kernelType %q", kt)
	}
	if mc.Spec.FIPS {
		unsupported("fips: true (enable AIX FIPS mode on the host instead)")
	}
	if ign != nil {
		if n := len(ign.Systemd.Units); n > 0 {
			names := make([]string, 0, n)
			for _, u := range ign.Systemd.Units {
				names = append(names, u.Name)
			}
			unsupported("systemd units %v (AIX services are SRC subsystems; ship their configuration as files)", names)
		}
		for _, u := range ign.Passwd.Users {
			if u.PasswordHash != nil && *u.PasswordHash != "" {
				if err := ValidatePasswordHash(*u.PasswordHash); err != nil {
					errs = append(errs, fmt.Errorf("user %s: %w", u.Name, err))
				}
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("MachineConfig %s cannot be applied to an AIX node: %w", mc.Name, errors.Join(errs...))
}

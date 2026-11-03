//go:build aix

package daemon

// Host-OS hooks for AIX nodes. See platform_default.go for the Linux versions
// and docs/aix.md for the design. The AIX commands themselves are run through
// pkg/daemon/aix, which logs every call and is unit-tested with a fake runner.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/aix"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"k8s.io/klog/v2"
)

const (
	// AIX OpenSSH reads ~/.ssh/authorized_keys; it has no authorized_keys.d.
	hostSSHKeyPath = constants.CoreUserSSHPath + "/authorized_keys"
	// Removed users are locked rather than given an empty (passwordless) entry.
	deconfiguredPasswordHash = aix.LockedPassword

	// aixCommandTimeout bounds a single SRC operation (stop, start, refresh).
	aixCommandTimeout = 5 * time.Minute
)

var (
	aixRunner aix.Runner = aix.ExecRunner{}
	aixSRC               = aix.NewSRC(aixRunner)
)

func aixContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), aixCommandTimeout)
}

// rebootCommand reboots the node with shutdown -Fr. Like systemd-run on Linux,
// the shutdown runs detached so that reboot() returns and the daemon can wait
// to be stopped by the shutdown sequence (/etc/rc.shutdown stops SRC
// subsystems, including the kubelet and this daemon). There is no ostree
// finalization to order against on AIX.
func rebootCommand(rationale string, _ bool) *exec.Cmd {
	klog.Infof("Rebooting AIX node: %s", rationale)
	return exec.Command("/usr/bin/sh", "-c", aix.CmdNohup+" "+aix.CmdShutdown+" -Fr >/dev/null 2>&1 &")
}

// getBootID derives a boot identifier from the system boot record.
func getBootID() (string, error) {
	ctx, cancel := aixContext()
	defer cancel()
	return aix.BootID(ctx, aixRunner)
}

func srcAction(unit, verb string, act func(context.Context, string) error) error {
	sub, err := aix.SubsystemForUnit(unit)
	if err != nil {
		return err
	}
	ctx, cancel := aixContext()
	defer cancel()
	if err := act(ctx, sub); err != nil {
		return fmt.Errorf("%s %s (SRC subsystem %s): %w", verb, unit, sub, err)
	}
	klog.Infof("%s SRC subsystem %s (for %s)", verb, sub, unit)
	return nil
}

func restartService(name string) error {
	return srcAction(name, "restart", aixSRC.Restart)
}

func reloadService(name string) error {
	sub, err := aix.SubsystemForUnit(name)
	if err != nil {
		return err
	}
	if aix.ReloadMeansRestart(sub) {
		klog.Infof("SRC subsystem %s cannot reload its configuration; restarting it instead", sub)
		return srcAction(name, "restart", aixSRC.Restart)
	}
	return srcAction(name, "reload", aixSRC.Reload)
}

// reloadDaemon is systemctl daemon-reload; SRC reads subsystem definitions
// from the ODM on every command, so there is nothing to reload.
func reloadDaemon() error {
	klog.V(2).Info("daemon-reload is a no-op on AIX (SRC definitions are read from the ODM)")
	return nil
}

func startService(name string) error {
	return srcAction(name, "start", aixSRC.Start)
}

func stopService(name string) error {
	return srcAction(name, "stop", aixSRC.Stop)
}

// mkdirAsCoreUser creates dir and any missing parents with mode 0700, owned by
// the core user. The daemon runs as root under SRC, so this is mkdir + chown
// (AIX has no runuser).
func mkdirAsCoreUser(dir string) error {
	uid, err := lookupUID(constants.CoreUserName)
	if err != nil {
		return err
	}
	gid, err := lookupGID(constants.CoreGroupName)
	if err != nil {
		return err
	}
	var missing []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, d)
		if d == filepath.Dir(d) {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		if err := os.Chown(missing[i], uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func getUserPasswordHash(user string) (string, error) {
	ctx, cancel := aixContext()
	defer cancel()
	return aix.GetPasswordHash(ctx, aixRunner, user), nil
}

func setUserPasswordHash(user, hash string) error {
	ctx, cancel := aixContext()
	defer cancel()
	return aix.SetPasswordHash(ctx, aixRunner, user, hash)
}

// runUpdateCATrust mirrors the CA anchors into the directory Go programs on
// AIX (containerd-aix, the kubelet, this daemon) load trusted roots from.
func runUpdateCATrust() (string, error) {
	changed, err := aix.SyncCATrust(aix.CATrustAnchorsDir, aix.SystemCertsDir)
	if err != nil {
		return "", err
	}
	if changed {
		klog.Infof("Synchronized %s into %s", aix.CATrustAnchorsDir, aix.SystemCertsDir)
	}
	return "", nil
}

// isFileOwnedByPackage asks the installp database (and the AIX Toolbox RPM
// database, when present) whether fpath shipped with the OS.
func isFileOwnedByPackage(fpath string) (bool, bool, error) {
	_, rpmErr := os.Stat(aix.CmdRpm)
	ctx, cancel := aixContext()
	defer cancel()
	owned, err := aix.IsFileOwnedByPackage(ctx, aixRunner, fpath, rpmErr == nil)
	return false, owned, err
}

func copyPreservingAttrs(src, dst string) error {
	return aix.CopyPreservingAttrs(src, dst)
}

// writeSystemLog writes to syslog through AIX logger(1).
func writeSystemLog(message string) error {
	ctx, cancel := aixContext()
	defer cancel()
	_, err := aixRunner.Run(ctx, aix.CmdLogger, "-t", "machine-config-daemon", "-p", "daemon.info", message)
	return err
}

// logHostServiceState logs the last boot and the state of the node's
// Kubernetes SRC subsystems at startup, the AIX counterpart of journalctl
// --list-boots and systemctl --failed.
func logHostServiceState(cmdRunner CommandRunner) {
	if out, err := cmdRunner.RunGetOut(aix.CmdWho, "-b"); err != nil {
		klog.Errorf("Reading last boot: %v", err)
	} else {
		klog.Info("who -b: " + strings.TrimSpace(string(out)))
	}
	if out, err := cmdRunner.RunGetOut(aix.CmdLssrc, "-g", aix.SubsystemGroup); err != nil {
		klog.Errorf("Listing SRC group %s: %v", aix.SubsystemGroup, err)
	} else {
		klog.Info("lssrc -g " + aix.SubsystemGroup + ":\n" + string(out))
	}
	if out, err := cmdRunner.RunGetOut(aix.CmdErrpt, "-d", "H,S", "-T", "PERM"); err == nil && len(out) > 0 {
		klog.Info("errpt (permanent hardware/software errors):\n" + string(out))
	}
}

// newHostConfigDriftMonitor returns a polling monitor: fsnotify has no AIX
// backend.
func newHostConfigDriftMonitor() ConfigDriftMonitor {
	return newPollingConfigDriftMonitor(defaultConfigDriftPollInterval)
}

// validateMachineConfigForHost rejects MachineConfig content that needs RHCOS.
func validateMachineConfigForHost(mc *mcfgv1.MachineConfig) error {
	if _, image := extractOCLImageFromMachineConfig(mc); image != "" {
		return fmt.Errorf("MachineConfig %s uses on-cluster layering image %s: %w", mc.Name, image, aix.ErrUnsupported)
	}
	ign, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("parsing Ignition config of %s: %w", mc.Name, err)
	}
	return aix.ValidateMachineConfig(mc, &ign)
}

// validateIgnitionForHost applies the AIX checks to a bare Ignition config
// passed with --once-from, which does not go through update().
func validateIgnitionForHost(cfg ign3types.Config) error {
	mc := &mcfgv1.MachineConfig{}
	mc.Name = "--once-from Ignition config"
	return aix.ValidateMachineConfig(mc, &cfg)
}

// kubeletKubeconfigRequired is false on AIX so that the daemon can be tested
// (standalone or attached to a cluster) before the kubelet port is installed.
const kubeletKubeconfigRequired = false

// adjustDiffForHost drops the OS-level parts of a diff. AIX nodes never act on
// them (validateMachineConfigForHost guarantees the target config does not ask
// for them), and in particular the RHCOS osImageURL that changes on every
// cluster upgrade must not make AIX nodes reboot.
func adjustDiffForHost(diff *machineConfigDiff) {
	diff.osUpdate = false
	diff.kargs = false
	diff.fips = false
	diff.kernelType = false
	diff.extensions = false
	diff.oclEnabled = false
	diff.revertFromOCL = false
}

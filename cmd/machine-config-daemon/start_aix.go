//go:build aix

package main

// On AIX the daemon runs in the global environment as the "mcd" SRC
// subsystem, not as a pod: containerd-aix WPARs cannot share the host PID
// space or modify the global environment, and the MCD DaemonSet only selects
// kubernetes.io/os=linux nodes. See docs/aix.md.
const (
	// The daemon already runs against the host root; there is nothing to chroot into.
	defaultRootMount = "/"
	// Credentials for the machine-config-daemon service account, installed
	// when the node joins the cluster.
	defaultKubeconfig   = "/etc/machine-config-daemon/kubeconfig"
	kubeconfigFlagUsage = "Kubeconfig file with the machine-config-daemon service account credentials"

	// PinnedImageSets drive CRI-O; AIX nodes run containerd-aix.
	hostSupportsPinnedImageSets = false
	// AIX nodes are never control-plane nodes, which host the internal release image registry.
	hostSupportsInternalReleaseImage = false
)

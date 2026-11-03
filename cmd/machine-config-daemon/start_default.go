//go:build !aix

package main

const (
	// defaultRootMount is where the MCD pod sees the host root filesystem.
	defaultRootMount = "/rootfs"
	// defaultKubeconfig is empty: the MCD pod uses its service account.
	defaultKubeconfig   = ""
	kubeconfigFlagUsage = "Kubeconfig file to access a remote cluster (testing only)"

	hostSupportsPinnedImageSets      = true
	hostSupportsInternalReleaseImage = true
)

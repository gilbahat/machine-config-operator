//go:build !aix

package osrelease

func hostRunningOSFromRoot(root string) (OperatingSystem, error) {
	return getOSFromOSRelease(root)
}

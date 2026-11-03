//go:build aix

package osrelease

import (
	"context"
	"fmt"
	"sync"
	"time"

	aixhost "github.com/openshift/machine-config-operator/pkg/daemon/aix"
)

var (
	aixOnce  sync.Once
	aixLevel aixhost.OSLevel
	aixErr   error
)

// hostRunningOSFromRoot identifies the AIX release with oslevel. The daemon
// runs in the global environment on AIX, so only the running root ("/") can be
// described.
func hostRunningOSFromRoot(root string) (OperatingSystem, error) {
	if root != "/" && root != "" {
		return OperatingSystem{}, fmt.Errorf("cannot identify the operating system under %s: the AIX daemon runs against the host root (/)", root)
	}
	aixOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		aixLevel, aixErr = aixhost.GetOSLevel(ctx, aixhost.ExecRunner{})
		if aixErr == nil {
			aixErr = aixLevel.CheckSupported()
		}
	})
	if aixErr != nil {
		return OperatingSystem{}, aixErr
	}
	return NewAIXOperatingSystem(aixLevel.Version), nil
}

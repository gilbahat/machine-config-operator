package aix

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"
)

const (
	// CATrustAnchorsDir is where MachineConfigs and the daemon place extra CA
	// certificates (the same path RHCOS uses, so cluster MachineConfigs and the
	// daemon's user CA bundle handling work unchanged).
	CATrustAnchorsDir = "/etc/pki/ca-trust/source/anchors"
	// SystemCertsDir is the directory Go's crypto/x509 scans for trusted roots
	// on AIX, alongside /var/ssl/certs/ca-bundle.crt. containerd-aix, the
	// kubelet port and this daemon are Go programs, so this is the trust store
	// that matters for image pulls and API access.
	SystemCertsDir = "/var/ssl/certs"
	// managedPrefix marks the files this package owns in SystemCertsDir.
	managedPrefix = "mco-anchor-"
)

// SyncCATrust is the AIX replacement for update-ca-trust. It mirrors every PEM
// file in anchorsDir into certsDir as "mco-anchor-<name>.pem" and removes
// mirrored files whose anchor is gone. Files it did not create are never
// touched. It returns whether anything changed.
func SyncCATrust(anchorsDir, certsDir string) (bool, error) {
	want := map[string][]byte{}
	entries, err := os.ReadDir(anchorsDir)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading CA anchors: %w", err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(anchorsDir, e.Name()))
		if err != nil {
			return false, fmt.Errorf("reading CA anchor %s: %w", e.Name(), err)
		}
		if blk, _ := pem.Decode(b); blk == nil || blk.Type != "CERTIFICATE" {
			klog.Warningf("skipping CA anchor %s: no PEM certificate", e.Name())
			continue
		}
		name := managedPrefix + strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".crt"), ".pem") + ".pem"
		want[name] = b
	}

	if err := os.MkdirAll(certsDir, 0o755); err != nil {
		return false, err
	}
	changed := false
	for name, b := range want {
		dst := filepath.Join(certsDir, name)
		if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, b) {
			continue
		}
		tmp := dst + ".mcdtmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return changed, err
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return changed, err
		}
		klog.Infof("Installed CA anchor %s into %s", name, certsDir)
		changed = true
	}

	existing, err := os.ReadDir(certsDir)
	if err != nil {
		return changed, err
	}
	for _, e := range existing {
		if !strings.HasPrefix(e.Name(), managedPrefix) {
			continue
		}
		if _, keep := want[e.Name()]; keep {
			continue
		}
		if err := os.Remove(filepath.Join(certsDir, e.Name())); err != nil && !os.IsNotExist(err) {
			return changed, err
		}
		klog.Infof("Removed stale CA anchor %s from %s", e.Name(), certsDir)
		changed = true
	}
	return changed, nil
}

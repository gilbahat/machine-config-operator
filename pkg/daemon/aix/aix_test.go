package aix

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner returns scripted results keyed by the full command line and
// records every call.
type fakeRunner struct {
	responses map[string][]response
	calls     []string
	inputs    []string
}

type response struct {
	out string
	err error
}

func newFakeRunner() *fakeRunner { return &fakeRunner{responses: map[string][]response{}} }

func (f *fakeRunner) on(cmdline, out string, err error) *fakeRunner {
	f.responses[cmdline] = append(f.responses[cmdline], response{out, err})
	return f
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.RunWithInput(ctx, nil, name, args...)
}

func (f *fakeRunner) RunWithInput(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	if input != nil {
		f.inputs = append(f.inputs, string(input))
	}
	rs, ok := f.responses[key]
	if !ok || len(rs) == 0 {
		return nil, fmt.Errorf("unexpected command %q", key)
	}
	r := rs[0]
	if len(rs) > 1 { // the last response repeats
		f.responses[key] = rs[1:]
	}
	return []byte(r.out), r.err
}

var errExit = errors.New("exit status 1")

const lssrcHeader = "Subsystem         Group            PID          Status \n"

func TestParseLssrc(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want SubsystemStatus
		err  error
	}{
		{"active with group", lssrcHeader + " sshd             tcpip            4194678      active\n",
			SubsystemStatus{Name: "sshd", Group: "tcpip", PID: 4194678, State: StateActive}, nil},
		{"inoperative with group", lssrcHeader + " kubelet          kubernetes                    inoperative\n",
			SubsystemStatus{Name: "kubelet", Group: "kubernetes", State: StateInoperative}, nil},
		{"active without group", lssrcHeader + " mcd                               5111            active\n",
			SubsystemStatus{Name: "mcd", PID: 5111, State: StateActive}, nil},
		{"inoperative without group", lssrcHeader + " mcd                                             inoperative\n",
			SubsystemStatus{Name: "mcd", State: StateInoperative}, nil},
		{"not on file", "0513-085 The foo Subsystem is not on file.\n", SubsystemStatus{}, ErrSubsystemNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseLssrc([]byte(c.out))
			if c.err != nil {
				assert.ErrorIs(t, err, c.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
	_, err := ParseLssrc([]byte(lssrcHeader))
	assert.Error(t, err)
}

func testSRC(r Runner) *SRC {
	return &SRC{Runner: r, PollInterval: time.Millisecond, StopTimeout: 20 * time.Millisecond, StartTimeout: 20 * time.Millisecond}
}

func TestSRCRestart(t *testing.T) {
	r := newFakeRunner().
		on("/usr/bin/lssrc -s containerd", lssrcHeader+" containerd kubernetes 42 active\n", nil).
		on("/usr/bin/lssrc -s containerd", lssrcHeader+" containerd kubernetes 42 stopping\n", nil).
		on("/usr/bin/lssrc -s containerd", lssrcHeader+" containerd kubernetes inoperative\n", nil).
		on("/usr/bin/lssrc -s containerd", lssrcHeader+" containerd kubernetes inoperative\n", nil).
		on("/usr/bin/lssrc -s containerd", lssrcHeader+" containerd kubernetes 77 active\n", nil).
		on("/usr/bin/stopsrc -s containerd", "0513-044 The containerd Subsystem was requested to stop.\n", nil).
		on("/usr/bin/startsrc -s containerd", "0513-059 The containerd Subsystem has been started. Subsystem PID is 77.\n", nil)
	require.NoError(t, testSRC(r).Restart(context.Background(), "containerd"))
	assert.Contains(t, r.calls, "/usr/bin/stopsrc -s containerd")
	assert.Contains(t, r.calls, "/usr/bin/startsrc -s containerd")
	assert.NotContains(t, r.calls, "/usr/bin/stopsrc -f -s containerd")
}

func TestSRCStopEscalatesToForce(t *testing.T) {
	r := newFakeRunner().
		on("/usr/bin/lssrc -s kubelet", lssrcHeader+" kubelet kubernetes 42 active\n", nil).
		on("/usr/bin/lssrc -s kubelet", lssrcHeader+" kubelet kubernetes 42 stopping\n", nil).
		on("/usr/bin/stopsrc -s kubelet", "", nil).
		on("/usr/bin/stopsrc -f -s kubelet", "", nil)
	// The scripted lssrc never reports inoperative: the normal stop times out,
	// the daemon escalates to stopsrc -f, and that wait times out too.
	err := testSRC(r).Stop(context.Background(), "kubelet")
	require.Error(t, err)
	assert.Contains(t, r.calls, "/usr/bin/stopsrc -f -s kubelet")
}

func TestSRCForcedStopSucceeds(t *testing.T) {
	r := newFakeRunner().
		on("/usr/bin/lssrc -s kubelet", lssrcHeader+" kubelet kubernetes 42 active\n", nil).
		on("/usr/bin/lssrc -s kubelet", lssrcHeader+" kubelet kubernetes 42 stopping\n", nil).
		on("/usr/bin/stopsrc -s kubelet", "", nil).
		on("/usr/bin/stopsrc -f -s kubelet", "", nil)
	s := testSRC(r)
	s.StopTimeout = 5 * time.Millisecond
	// Once stopsrc -f has been issued, lssrc reports inoperative.
	stopped := false
	wrapped := &hookRunner{Runner: r, after: func(cmd string) {
		if cmd == "/usr/bin/stopsrc -f -s kubelet" && !stopped {
			stopped = true
			r.responses["/usr/bin/lssrc -s kubelet"] = []response{{lssrcHeader + " kubelet kubernetes inoperative\n", nil}}
		}
	}}
	s.Runner = wrapped
	require.NoError(t, s.Stop(context.Background(), "kubelet"))
}

type hookRunner struct {
	Runner
	after func(cmd string)
}

func (h *hookRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := h.Runner.Run(ctx, name, args...)
	h.after(strings.Join(append([]string{name}, args...), " "))
	return out, err
}

func TestSRCStartAlreadyActive(t *testing.T) {
	r := newFakeRunner().on("/usr/bin/lssrc -s kubelet", lssrcHeader+" kubelet kubernetes 42 active\n", nil)
	require.NoError(t, testSRC(r).Start(context.Background(), "kubelet"))
	assert.NotContains(t, r.calls, "/usr/bin/startsrc -s kubelet")
}

func TestSRCMissingSubsystem(t *testing.T) {
	r := newFakeRunner().on("/usr/bin/lssrc -s nope", "0513-085 The nope Subsystem is not on file.\n", errExit)
	err := testSRC(r).Restart(context.Background(), "nope")
	assert.ErrorIs(t, err, ErrSubsystemNotFound)
}

func TestSRCReloadFallsBackToSIGHUP(t *testing.T) {
	r := newFakeRunner().
		on("/usr/bin/lssrc -s sshd", lssrcHeader+" sshd tcpip 4194678 active\n", nil).
		on("/usr/bin/refresh -s sshd", "0513-005 The Subsystem, sshd, only supports signal communication.\n", errExit).
		on("/usr/bin/kill -HUP 4194678", "", nil)
	require.NoError(t, testSRC(r).Reload(context.Background(), "sshd"))
	assert.Contains(t, r.calls, "/usr/bin/kill -HUP 4194678")
}

func TestSRCReloadInoperative(t *testing.T) {
	r := newFakeRunner().on("/usr/bin/lssrc -s sshd", lssrcHeader+" sshd tcpip inoperative\n", nil)
	assert.Error(t, testSRC(r).Reload(context.Background(), "sshd"))
}

func TestValidateSubsystemName(t *testing.T) {
	assert.NoError(t, ValidateSubsystemName("containerd"))
	assert.NoError(t, ValidateSubsystemName("rpc.mountd"))
	assert.Error(t, ValidateSubsystemName("-a"))
	assert.Error(t, ValidateSubsystemName("a b"))
	assert.Error(t, ValidateSubsystemName(strings.Repeat("x", 31)))
}

func TestSubsystemForUnit(t *testing.T) {
	for unit, want := range map[string]string{
		"crio.service":    SubsystemContainerd,
		"crio":            SubsystemContainerd,
		"kubelet.service": SubsystemKubelet,
		"sshd.service":    "sshd",
	} {
		got, err := SubsystemForUnit(unit)
		require.NoError(t, err, unit)
		assert.Equal(t, want, got, unit)
	}
	for _, unit := range []string{"kubelet-dependencies.target", "foo.socket", "foo.timer", "-x.service"} {
		_, err := SubsystemForUnit(unit)
		assert.Error(t, err, unit)
	}
	assert.True(t, ReloadMeansRestart(SubsystemContainerd))
	assert.False(t, ReloadMeansRestart("sshd"))
}

func TestBootID(t *testing.T) {
	r := newFakeRunner().on("/usr/bin/who -b", "   .        system boot  Nov 03 10:21    \n", nil)
	id1, err := BootID(context.Background(), r)
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, id1)

	r2 := newFakeRunner().on("/usr/bin/who -b", "   .        system boot Nov 03 10:21\n", nil)
	id2, err := BootID(context.Background(), r2)
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "whitespace must not change the boot ID")

	r3 := newFakeRunner().on("/usr/bin/who -b", "   .        system boot  Nov 03 11:02\n", nil)
	id3, err := BootID(context.Background(), r3)
	require.NoError(t, err)
	assert.NotEqual(t, id1, id3)

	_, err = BootID(context.Background(), newFakeRunner().on("/usr/bin/who -b", "\n", nil))
	assert.Error(t, err)
}

func TestParseOSLevel(t *testing.T) {
	lvl, err := ParseOSLevel([]byte("7300-02-01-2346\n"))
	require.NoError(t, err)
	assert.Equal(t, "7.3", lvl.Version)
	assert.Equal(t, "02", lvl.TL)
	assert.NoError(t, lvl.CheckSupported())

	old, err := ParseOSLevel([]byte("7100-05-06-2015"))
	require.NoError(t, err)
	assert.Error(t, old.CheckSupported())

	_, err = ParseOSLevel([]byte("7.3.0.0"))
	assert.Error(t, err)
}

func TestIsFileOwnedByPackage(t *testing.T) {
	owned := "  File                                        Fileset               Type\n" +
		"  ----------------------------------------------------------------------------\n" +
		"  /etc/hosts                                  bos.net.tcp.client    File\n"
	r := newFakeRunner().on("/usr/bin/lslpp -w /etc/hosts", owned, nil)
	ok, err := IsFileOwnedByPackage(context.Background(), r, "/etc/hosts", false)
	require.NoError(t, err)
	assert.True(t, ok)

	noMatch := "lslpp: 0504-308  lslpp:  No match was found for the SWVPD data search.\n"
	r = newFakeRunner().
		on("/usr/bin/lslpp -w /opt/freeware/etc/foo", noMatch, errExit).
		on("/usr/bin/rpm -qf /opt/freeware/etc/foo", "foo-1.0-1.aix7.3.ppc\n", nil)
	ok, err = IsFileOwnedByPackage(context.Background(), r, "/opt/freeware/etc/foo", true)
	require.NoError(t, err)
	assert.True(t, ok)

	r = newFakeRunner().on("/usr/bin/lslpp -w /etc/new", noMatch, errExit)
	ok, err = IsFileOwnedByPackage(context.Background(), r, "/etc/new", false)
	require.NoError(t, err)
	assert.False(t, ok)

	r = newFakeRunner().on("/usr/bin/lslpp -w /etc/x", "odd failure", errExit)
	_, err = IsFileOwnedByPackage(context.Background(), r, "/etc/x", false)
	assert.Error(t, err)
}

func TestPasswords(t *testing.T) {
	const good = "{ssha512}06$0123456789abcdef$AbCdEf0123./xyz"
	assert.NoError(t, ValidatePasswordHash(good))
	assert.NoError(t, ValidatePasswordHash(LockedPassword))
	assert.NoError(t, ValidatePasswordHash("ab01FAX.bQRSU"))
	err := ValidatePasswordHash("$6$salt$abcdef")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Linux crypt(3)")
	assert.Error(t, ValidatePasswordHash("plaintext!"))

	hash, ok := ParseLssecPassword([]byte("core password="+good+"\n"), "core")
	assert.True(t, ok)
	assert.Equal(t, good, hash)

	r := newFakeRunner().on("/usr/bin/chpasswd -e -c", "", nil)
	require.NoError(t, SetPasswordHash(context.Background(), r, "core", good))
	assert.Equal(t, []string{"core:" + good + "\n"}, r.inputs)

	assert.Error(t, SetPasswordHash(context.Background(), newFakeRunner(), "core", "$6$x$y"))
	assert.Error(t, SetPasswordHash(context.Background(), newFakeRunner(), "co:re", good))

	r = newFakeRunner().on("/usr/bin/lssec -f /etc/security/passwd -s core -a password", "core password="+good+"\n", nil)
	assert.Equal(t, good, GetPasswordHash(context.Background(), r, "core"))
	r = newFakeRunner().on("/usr/bin/lssec -f /etc/security/passwd -s core -a password", "3004-687 User \"core\" does not exist.\n", errExit)
	assert.Equal(t, "", GetPasswordHash(context.Background(), r, "core"))
}

func TestValidateMachineConfig(t *testing.T) {
	mc := &mcfgv1.MachineConfig{}
	mc.Name = "rendered-aix-abc"
	mc.Spec.OSImageURL = "quay.io/openshift/rhcos@sha256:abc"
	ign := &ign3types.Config{}
	assert.NoError(t, ValidateMachineConfig(mc, ign), "osImageURL alone must be accepted")

	mc.Spec.KernelType = "default"
	assert.NoError(t, ValidateMachineConfig(mc, ign))

	bad := mc.DeepCopy()
	bad.Spec.KernelArguments = []string{"nosmt"}
	bad.Spec.Extensions = []string{"usbguard"}
	bad.Spec.KernelType = "realtime"
	bad.Spec.FIPS = true
	contents := "[Service]\n"
	badIgn := &ign3types.Config{}
	badIgn.Systemd.Units = []ign3types.Unit{{Name: "foo.service", Contents: &contents}}
	linuxHash := "$6$salt$hash"
	badIgn.Passwd.Users = []ign3types.PasswdUser{{Name: "core", PasswordHash: &linuxHash}}
	err := ValidateMachineConfig(bad, badIgn)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupported)
	for _, s := range []string{"rendered-aix-abc", "kernelArguments", "extensions", "realtime", "fips", "foo.service", "user core"} {
		assert.Contains(t, err.Error(), s)
	}
}

func TestCopyPreservingAttrs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("hello"), 0o640))
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(src, mtime, mtime))

	dst := filepath.Join(dir, "dst")
	require.NoError(t, CopyPreservingAttrs(src, dst))
	b, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
	fi, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), fi.Mode().Perm())
	assert.True(t, fi.ModTime().Equal(mtime))
	sfi, _ := os.Stat(src)
	assert.Equal(t, sfi.Sys().(*syscall.Stat_t).Uid, fi.Sys().(*syscall.Stat_t).Uid)

	// overwrite an existing destination
	require.NoError(t, os.WriteFile(src, []byte("world"), 0o600))
	require.NoError(t, CopyPreservingAttrs(src, dst))
	b, _ = os.ReadFile(dst)
	assert.Equal(t, "world", string(b))

	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink("src", link))
	linkCopy := filepath.Join(dir, "linkcopy")
	require.NoError(t, CopyPreservingAttrs(link, linkCopy))
	target, err := os.Readlink(linkCopy)
	require.NoError(t, err)
	assert.Equal(t, "src", target)

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".mcdtmp"), "temporary file left behind")
	}
}

func TestSyncCATrust(t *testing.T) {
	anchors := filepath.Join(t.TempDir(), "anchors")
	certs := filepath.Join(t.TempDir(), "certs")
	require.NoError(t, os.MkdirAll(anchors, 0o755))
	require.NoError(t, os.MkdirAll(certs, 0o755))
	cert := "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
	require.NoError(t, os.WriteFile(filepath.Join(anchors, "openshift-config-user-ca-bundle.crt"), []byte(cert), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(anchors, "junk.txt"), []byte("not a cert"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(certs, "ca-bundle.crt"), []byte("system"), 0o644))

	changed, err := SyncCATrust(anchors, certs)
	require.NoError(t, err)
	assert.True(t, changed)
	b, err := os.ReadFile(filepath.Join(certs, "mco-anchor-openshift-config-user-ca-bundle.pem"))
	require.NoError(t, err)
	assert.Equal(t, cert, string(b))
	_, err = os.Stat(filepath.Join(certs, "mco-anchor-junk.txt.pem"))
	assert.True(t, os.IsNotExist(err))

	changed, err = SyncCATrust(anchors, certs)
	require.NoError(t, err)
	assert.False(t, changed, "second sync must be a no-op")

	require.NoError(t, os.Remove(filepath.Join(anchors, "openshift-config-user-ca-bundle.crt")))
	changed, err = SyncCATrust(anchors, certs)
	require.NoError(t, err)
	assert.True(t, changed)
	_, err = os.Stat(filepath.Join(certs, "mco-anchor-openshift-config-user-ca-bundle.pem"))
	assert.True(t, os.IsNotExist(err))
	sys, err := os.ReadFile(filepath.Join(certs, "ca-bundle.crt"))
	require.NoError(t, err)
	assert.Equal(t, "system", string(sys), "unmanaged files must be left alone")

	// A missing anchors directory just clears the managed files.
	_, err = SyncCATrust(filepath.Join(t.TempDir(), "absent"), certs)
	require.NoError(t, err)
}

package aix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// ParseWhoBoot extracts the boot timestamp from `who -b` output, e.g.
//
//	.        system boot  Nov 03 10:21
func ParseWhoBoot(out []byte) (string, error) {
	const marker = "system boot"
	for _, line := range strings.Split(string(out), "\n") {
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		ts := strings.Join(strings.Fields(line[i+len(marker):]), " ")
		if ts == "" {
			break
		}
		return ts, nil
	}
	return "", fmt.Errorf("no system boot record in who -b output %q", strings.TrimSpace(string(out)))
}

// BootID returns a stable identifier for the current boot. AIX has no
// equivalent of Linux's /proc/sys/kernel/random/boot_id, so the ID is derived
// from the boot record in /etc/utmp as reported by `who -b`. The daemon only
// compares boot IDs for equality, to tell whether the node rebooted.
func BootID(ctx context.Context, r Runner) (string, error) {
	out, err := r.Run(ctx, CmdWho, "-b")
	if err != nil {
		return "", fmt.Errorf("reading boot time: %w", err)
	}
	ts, err := ParseWhoBoot(out)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte("aix-boot:" + ts))
	h := hex.EncodeToString(sum[:16])
	// Format like a UUID so it looks like the Linux boot_id in logs and annotations.
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}

// OSLevel is the parsed output of `oslevel -s`, e.g. 7300-02-01-2346.
type OSLevel struct {
	Raw        string
	Version    string // "7.3"
	TL         string // technology level, "02"
	SP         string // service pack, "01"
	BuildDate  string // "2346"
	MajorMinor [2]int
}

var oslevelRE = regexp.MustCompile(`^(\d)(\d)00-(\d{2})-(\d{2})-(\d{4})$`)

// ParseOSLevel parses `oslevel -s` output.
func ParseOSLevel(out []byte) (OSLevel, error) {
	raw := strings.TrimSpace(string(out))
	m := oslevelRE.FindStringSubmatch(raw)
	if m == nil {
		return OSLevel{}, fmt.Errorf("unexpected oslevel -s output %q", raw)
	}
	lvl := OSLevel{Raw: raw, Version: m[1] + "." + m[2], TL: m[3], SP: m[4], BuildDate: m[5]}
	lvl.MajorMinor = [2]int{int(m[1][0] - '0'), int(m[2][0] - '0')}
	return lvl, nil
}

// GetOSLevel runs `oslevel -s`.
func GetOSLevel(ctx context.Context, r Runner) (OSLevel, error) {
	out, err := r.Run(ctx, CmdOslevel, "-s")
	if err != nil {
		return OSLevel{}, err
	}
	return ParseOSLevel(out)
}

// MinimumSupportedOSLevel is the oldest AIX release Go supports.
var MinimumSupportedOSLevel = [2]int{7, 2}

// CheckSupported returns an error for AIX releases older than 7.2.
func (l OSLevel) CheckSupported() error {
	if l.MajorMinor[0] < MinimumSupportedOSLevel[0] ||
		(l.MajorMinor[0] == MinimumSupportedOSLevel[0] && l.MajorMinor[1] < MinimumSupportedOSLevel[1]) {
		return fmt.Errorf("AIX %s is not supported; AIX %d.%d or later is required", l.Version, MinimumSupportedOSLevel[0], MinimumSupportedOSLevel[1])
	}
	return nil
}

const msgLslppNoMatch = "0504-308" // lslpp: No match was found for the SWVPD data search.

// ParseLslppW reports whether `lslpp -w <path>` output lists path as owned by
// a fileset. The data rows are "<path> <fileset> <type>".
func ParseLslppW(out []byte, path string) bool {
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == path {
			return true
		}
	}
	return false
}

// IsFileOwnedByPackage reports whether path belongs to an installp fileset or,
// when the AIX Toolbox RPM database is present, to an RPM. This decides
// whether the daemon keeps a pristine ".orig" copy before overwriting a file,
// exactly as it does for RPM-owned files on RHEL.
func IsFileOwnedByPackage(ctx context.Context, r Runner, path string, haveRPM bool) (bool, error) {
	out, err := r.Run(ctx, CmdLslpp, "-w", path)
	switch {
	case err == nil && ParseLslppW(out, path):
		return true, nil
	case err != nil && !hasMessageCode(out, msgLslppNoMatch):
		return false, fmt.Errorf("checking fileset ownership of %s: %w", path, err)
	}
	if !haveRPM {
		return false, nil
	}
	if _, err := r.Run(ctx, CmdRpm, "-qf", path); err == nil {
		return true, nil
	}
	return false, nil
}

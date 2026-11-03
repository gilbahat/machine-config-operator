package aix

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/klog/v2"
)

// LockedPassword is the password field value that disables password logins.
// The daemon writes it for users with no passwordHash and for users removed
// from the MachineConfig. (On Linux the daemon clears the hash instead; on AIX
// an empty field would allow a login without a password, so it is locked.)
const LockedPassword = "*"

var (
	// AIX Loadable Password Algorithm hashes, e.g. {ssha512}06$salt$hash.
	lpaHashRE = regexp.MustCompile(`^\{(smd5|ssha1|ssha256|ssha512)\}[0-9]{2}\$[./0-9A-Za-z]+\$[./0-9A-Za-z]+$`)
	// Traditional 13-character DES crypt.
	desHashRE = regexp.MustCompile(`^[./0-9A-Za-z]{13}$`)
	// Linux-style modular crypt, e.g. $6$salt$hash.
	modularCryptRE = regexp.MustCompile(`^\$[0-9a-z]+\$`)
)

// ValidatePasswordHash rejects hashes /etc/security/passwd cannot hold.
// MachineConfigs written for RHCOS usually carry SHA-512 crypt ($6$) hashes,
// which AIX's default authentication does not understand; writing one would
// silently lock the user out, so it is refused with an explanation instead.
func ValidatePasswordHash(hash string) error {
	switch {
	case hash == LockedPassword, lpaHashRE.MatchString(hash), desHashRE.MatchString(hash):
		return nil
	case modularCryptRE.MatchString(hash):
		return fmt.Errorf("password hash uses the Linux crypt(3) format %q, which AIX does not accept; "+
			"generate an AIX hash (for example {ssha512} with pwd_algorithm=ssha512 in /etc/security/login.cfg) for the AIX pool's MachineConfig",
			strings.SplitN(hash, "$", 3)[1])
	default:
		return fmt.Errorf("password hash is not an AIX password hash ({ssha512}, {ssha256}, {ssha1}, {smd5} or DES crypt)")
	}
}

// ParseLssecPassword parses `lssec -f /etc/security/passwd -s <user> -a password`
// output, which looks like "core password=<hash>".
func ParseLssecPassword(out []byte, user string) (string, bool) {
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != user {
			continue
		}
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(f, "password="); ok {
				return v, true
			}
		}
	}
	return "", false
}

// GetPasswordHash returns the hash stored in /etc/security/passwd for user, or
// "" if it cannot be read (the caller then just sets it).
func GetPasswordHash(ctx context.Context, r Runner, user string) string {
	out, err := r.Run(ctx, CmdLssec, "-f", "/etc/security/passwd", "-s", user, "-a", "password")
	if err != nil {
		klog.V(2).Infof("could not read password stanza for %s: %v", user, err)
		return ""
	}
	hash, _ := ParseLssecPassword(out, user)
	return hash
}

// SetPasswordHash stores an already-encrypted hash for user with chpasswd -e,
// clearing the ADMCHG flag (-c) so the user is not forced to change it.
func SetPasswordHash(ctx context.Context, r Runner, user, hash string) error {
	if strings.ContainsAny(user, ":\n") || strings.ContainsAny(hash, ":\n") {
		return fmt.Errorf("refusing to set password for %q: user or hash contains ':' or a newline", user)
	}
	if err := ValidatePasswordHash(hash); err != nil {
		return fmt.Errorf("setting password for %s: %w", user, err)
	}
	if _, err := r.RunWithInput(ctx, []byte(user+":"+hash+"\n"), CmdChpasswd, "-e", "-c"); err != nil {
		return fmt.Errorf("setting password for %s: %w", user, err)
	}
	return nil
}

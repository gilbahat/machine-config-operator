package aix

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// CopyPreservingAttrs copies src to dst keeping its mode, owner and times, and
// copies a symlink as a symlink. It replaces the GNU `cp -a --reflink=auto`
// the daemon uses on Linux for ".orig" backups; AIX cp has neither flag.
// dst is written to a temporary file and renamed into place.
func CopyPreservingAttrs(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".mcdtmp")
	_ = os.Remove(tmp)

	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, tmp); err != nil {
			return err
		}
	case fi.Mode().IsRegular():
		if err := copyRegular(src, tmp, fi.Mode().Perm()); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	default:
		return fmt.Errorf("cannot copy %s: unsupported file type %s", src, fi.Mode().Type())
	}

	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if err := os.Lchown(tmp, int(st.Uid), int(st.Gid)); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("preserving owner of %s: %w", src, err)
		}
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		// chown may clear setuid/setgid bits; restore the full mode afterwards.
		if err := os.Chmod(tmp, fi.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Chtimes(tmp, fi.ModTime(), fi.ModTime()); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func copyRegular(src, dst string, perm os.FileMode) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); retErr == nil {
			retErr = cerr
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

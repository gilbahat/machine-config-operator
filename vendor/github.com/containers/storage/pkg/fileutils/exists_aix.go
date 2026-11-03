//go:build aix

package fileutils

import "os"

// Exists checks whether a file or directory exists at the given path.
// If the path is a symlink, the symlink is followed.
func Exists(path string) error {
	_, err := os.Stat(path)
	return err
}

// Lexists checks whether a file or directory exists at the given path.
// If the path is a symlink, the symlink itself is checked.
func Lexists(path string) error {
	_, err := os.Lstat(path)
	return err
}

//go:build aix

package system

import "syscall"

// fromStatT converts a syscall.Stat_t type to a system.Stat_t type.
// AIX syscall.Stat_t.Mtim is StTimespec_t {Sec int64, Nsec int32}, not
// syscall.Timespec, so it is converted field by field.
func fromStatT(s *syscall.Stat_t) (*StatT, error) {
	return &StatT{
		size: s.Size,
		mode: uint32(s.Mode),
		uid:  s.Uid,
		gid:  s.Gid,
		rdev: uint64(s.Rdev),
		mtim: syscall.Timespec{Sec: s.Mtim.Sec, Nsec: int64(s.Mtim.Nsec)},
		dev:  uint64(s.Dev),
	}, nil
}

//go:build darwin

package media

import (
	"os"
	"syscall"
	"time"
)

func filesystemCreatedAt(info os.FileInfo) time.Time {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if stat.Birthtimespec.Sec != 0 || stat.Birthtimespec.Nsec != 0 {
			return time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec)
		}
	}
	return time.Time{}
}

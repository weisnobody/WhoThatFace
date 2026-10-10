//go:build !darwin

package media

import (
	"os"
	"time"
)

func filesystemCreatedAt(os.FileInfo) time.Time {
	return time.Time{}
}

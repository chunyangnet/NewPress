//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package dirlock

import (
	"errors"
	"os"
)

func lockFile(*os.File) error {
	return errors.New("process directory locking is not supported on this operating system")
}

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package app

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryPlatformFileLock attempts a non-blocking BSD-style advisory lock.
func tryPlatformFileLock(file *os.File, exclusive bool) (bool, error) {
	operation := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		operation = unix.LOCK_EX | unix.LOCK_NB
	}
	if err := unix.Flock(int(file.Fd()), operation); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// unlockPlatformFile releases a Unix advisory lock.
func unlockPlatformFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

//go:build darwin || linux

package pending

import (
	"errors"
	"os"
	"syscall"
)

const checkpointPlatformSupported = true
const checkpointOpenFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

func checkpointOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func checkpointSingleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func lockCheckpoint(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrBusy
	}
	if err != nil {
		return errors.New("cannot acquire checkpoint preparation lock")
	}
	return nil
}

func unlockCheckpoint(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

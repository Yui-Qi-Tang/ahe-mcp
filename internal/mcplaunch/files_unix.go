//go:build linux || darwin

package mcplaunch

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func readProtected(path string, limit int64) ([]byte, error) {
	file, err := openTrusted(path, false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > limit {
		return nil, errProtected
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(body)) != info.Size() {
		return nil, errProtected
	}
	return body, nil
}

func checkExecutable(path string) error {
	file, err := openTrusted(path, true)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return errProtected
	}
	return nil
}

func openTrusted(path string, executable bool) (*os.File, error) {
	if !absolutePath(path) || os.Getuid() != os.Geteuid() || trustedParents(path) != nil {
		return nil, errProtected
	}
	before, err := os.Lstat(path)
	if err != nil || !trustedFile(before, executable) {
		return nil, errProtected
	}
	// O_NONBLOCK prevents a concurrent replacement with a FIFO from blocking;
	// O_NOFOLLOW and the opened descriptor's metadata reject link substitution.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errProtected
	}
	after, err := file.Stat()
	if err != nil || !trustedFile(after, executable) || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, errProtected
	}
	return file, nil
}

func trustedFile(info os.FileInfo, executable bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return false
	}
	if !executable {
		return stat.Uid == uint32(os.Getuid()) && info.Mode().Perm() == 0600
	}
	return (stat.Uid == uint32(os.Getuid()) || stat.Uid == 0) && info.Mode().Perm()&0022 == 0 && info.Mode().Perm()&0111 != 0
}

func trustedParents(path string) error {
	parts := strings.Split(strings.TrimPrefix(filepath.Dir(path), "/"), "/")
	directory := "/"
	requirePrivateChild := false
	for index := -1; index < len(parts); index++ {
		if index >= 0 && parts[index] != "" {
			directory = filepath.Join(directory, parts[index])
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errProtected
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != uint32(os.Getuid()) && stat.Uid != 0) {
			return errProtected
		}
		if requirePrivateChild && (stat.Uid != uint32(os.Getuid()) || info.Mode().Perm() != 0700) {
			return errProtected
		}
		requirePrivateChild = false
		if info.Mode().Perm()&0022 != 0 {
			if stat.Uid != 0 || info.Mode()&os.ModeSticky == 0 || (directory != "/tmp" && directory != "/private/tmp") {
				return errProtected
			}
			requirePrivateChild = true
		}
	}
	if requirePrivateChild {
		return errProtected
	}
	return nil
}

func replaceProcess(path string, env []string) error {
	// The protected installation directory is the operator trust boundary.
	// This is not binary signing or protection against its owning operator.
	if err := syscall.Exec(path, []string{path}, env); err != nil {
		return errExec
	}
	return nil
}

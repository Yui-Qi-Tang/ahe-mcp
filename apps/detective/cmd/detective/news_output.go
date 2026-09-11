package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// A report reserves one fresh file before an attempt. It is not a reusable
// checkpoint, lock, or atomic publication protocol; failures retain the file.
type newsOutput struct {
	path     string
	root     *os.Root
	file     *os.File
	parent   os.FileInfo
	identity os.FileInfo
}

func newsPlain(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func newsModelNameSafe(value string) bool { return newsPlain(value, 200) }

// Preflight prevents credentials from entering the report. The Desktop model
// transport independently enforces the same loopback restriction at every dial.
func newsEndpointSafe(value string) bool {
	if !newsPlain(value, 2048) {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || (u.Path != "/v1" && u.Path != "/v1/") {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func newsOwned(info os.FileInfo) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(owner.Uid) == os.Getuid()
}

func newsAncestorsSafe(path string) bool {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if current == filepath.Dir(current) {
			return true
		}
	}
}

func reserveNewsOutput(path string) (*newsOutput, error) {
	bad := errors.New("unsafe or existing news output")
	if !newsPlain(path, 4096) || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, bad
	}
	parent := filepath.Dir(path)
	if !newsAncestorsSafe(parent) {
		return nil, bad
	}
	info, err := os.Lstat(parent)
	if err != nil || info.Mode().Perm() != 0o700 || !newsOwned(info) {
		return nil, bad
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, bad
	}
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(info, pinned) {
		root.Close()
		return nil, bad
	}
	file, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		root.Close()
		return nil, bad
	}
	identity, err := file.Stat()
	output := &newsOutput{path: path, root: root, file: file, parent: info, identity: identity}
	if err != nil || !identity.Mode().IsRegular() || identity.Mode().Perm() != 0o600 || !newsOwned(identity) || !output.current() {
		output.close()
		return nil, bad
	}
	return output, nil
}

func (o *newsOutput) current() bool {
	if !newsAncestorsSafe(filepath.Dir(o.path)) {
		return false
	}
	parent, err := os.Lstat(filepath.Dir(o.path))
	if err != nil || !os.SameFile(o.parent, parent) || parent.Mode().Perm() != 0o700 || !newsOwned(parent) {
		return false
	}
	info, err := o.root.Lstat(filepath.Base(o.path))
	if err != nil || !os.SameFile(o.identity, info) || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !newsOwned(info) {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func (o *newsOutput) write(report any) error {
	if o.file == nil || !o.current() {
		return errors.New("news output changed")
	}
	reserved, err := o.file.Stat()
	if err != nil || reserved.Size() != 0 {
		return errors.New("news output reservation is no longer empty")
	}
	body, err := json.MarshalIndent(report, "", "  ")
	// Full field citations may repeat up to three times and JSON escaping can
	// expand their bytes; this bound includes every valid input projection.
	if err != nil || len(body) > 2<<20 {
		return errors.New("news report exceeds bound")
	}
	body = append(body, '\n')
	n, err := o.file.Write(body)
	if err != nil {
		return err
	}
	if n != len(body) {
		return io.ErrShortWrite
	}
	if err := o.file.Sync(); err != nil {
		return err
	}
	if !o.current() {
		return errors.New("news output changed while saving")
	}
	if err := o.file.Close(); err != nil {
		return err
	}
	o.file = nil
	dir, err := o.root.Open(".")
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	return errors.Join(syncErr, dir.Close())
}

func (o *newsOutput) close() {
	if o.file != nil {
		_ = o.file.Close()
		o.file = nil
	}
	if o.root != nil {
		_ = o.root.Close()
		o.root = nil
	}
}

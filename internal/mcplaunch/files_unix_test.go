//go:build linux || darwin

package mcplaunch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func protectedFixtureDirectory(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve synthetic test directory: %v", err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatalf("protect synthetic test directory: %v", err)
	}
	return directory
}

func writeProtectedFixture(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("write synthetic fixture: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("set synthetic fixture permissions: %v", err)
	}
}

func TestProtectedFilesAndExecutable(t *testing.T) {
	directory := protectedFixtureDirectory(t)
	path := filepath.Join(directory, "credential")
	writeProtectedFixture(t, path, []byte("synthetic credential"), 0600)
	if body, err := readProtected(path, 64); err != nil || string(body) != "synthetic credential" {
		t.Fatal("protected regular file rejected")
	}
	for _, mode := range []os.FileMode{0400, 0640, 0644, 0660, 0700} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("set rejected fixture permissions: %v", err)
		}
		if _, err := readProtected(path, 64); !errors.Is(err, errProtected) {
			t.Fatalf("non-0600 fixture mode %v accepted", mode)
		}
	}
	writeProtectedFixture(t, path, []byte(strings.Repeat("x", 65)), 0600)
	if _, err := readProtected(path, 64); !errors.Is(err, errProtected) {
		t.Fatal("oversize credential accepted")
	}
	writeProtectedFixture(t, path, nil, 0600)
	if _, err := readProtected(path, 64); !errors.Is(err, errProtected) {
		t.Fatal("empty credential accepted")
	}
	if _, err := readProtected(directory, 64); !errors.Is(err, errProtected) {
		t.Fatal("directory accepted as a credential")
	}
	binary := filepath.Join(directory, "ahe-query-mcp")
	for _, mode := range []os.FileMode{0700, 0750, 0755} {
		writeProtectedFixture(t, binary, []byte("synthetic executable fixture"), mode)
		if err := checkExecutable(binary); err != nil {
			t.Fatalf("protected executable mode %v rejected", mode)
		}
	}
	for _, mode := range []os.FileMode{0600, 0770, 0777} {
		writeProtectedFixture(t, binary, []byte("synthetic executable fixture"), mode)
		if err := checkExecutable(binary); !errors.Is(err, errProtected) {
			t.Fatalf("unsafe executable mode %v accepted", mode)
		}
	}
}

func TestProtectedPathsRejectLinksAndWritableParents(t *testing.T) {
	directory := protectedFixtureDirectory(t)
	path := filepath.Join(directory, "credential")
	writeProtectedFixture(t, path, []byte("synthetic credential"), 0600)
	link := filepath.Join(directory, "credential-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtected(link, 64); !errors.Is(err, errProtected) {
		t.Fatal("symlink credential accepted")
	}
	parentLink := filepath.Join(directory, "parent-link")
	if err := os.Symlink(directory, parentLink); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtected(filepath.Join(parentLink, "credential"), 64); !errors.Is(err, errProtected) {
		t.Fatal("symlink ancestor accepted")
	}
	for _, mode := range []os.FileMode{0770, 0777, 0777 | os.ModeSticky} {
		if err := os.Chmod(directory, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readProtected(path, 64); !errors.Is(err, errProtected) {
			t.Fatalf("writable ancestor mode %v accepted", mode)
		}
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtected("relative/credential", 64); !errors.Is(err, errProtected) {
		t.Fatal("relative protected path accepted")
	}
}

func TestProtectedRootStickyDirectoryBoundary(t *testing.T) {
	base := "/tmp"
	if runtime.GOOS == "darwin" {
		base = "/private/tmp"
	}
	directory, err := os.MkdirTemp(base, "ahe-launch-protection-")
	if err != nil {
		t.Fatalf("create private sticky-root fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove owned sticky-root fixture: %v", err)
		}
	})
	path := filepath.Join(directory, "credential")
	writeProtectedFixture(t, path, []byte("synthetic"), 0600)
	if _, err := readProtected(path, 64); err != nil {
		t.Fatal("private child below system sticky directory rejected")
	}
	if err := trustedParents(filepath.Join(base, "unprotected-direct-file")); !errors.Is(err, errProtected) {
		t.Fatal("direct file below writable system directory accepted")
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtected(path, 64); !errors.Is(err, errProtected) {
		t.Fatal("non-private child below system sticky directory accepted")
	}
}

type metadataFileInfo struct {
	os.FileInfo
	metadata syscall.Stat_t
	mode     os.FileMode
}

func (info metadataFileInfo) Sys() any { return &info.metadata }
func (info metadataFileInfo) Mode() os.FileMode {
	if info.mode != 0 {
		return info.mode
	}
	return info.FileInfo.Mode()
}

func TestProtectedFileOwnership(t *testing.T) {
	directory := protectedFixtureDirectory(t)
	path := filepath.Join(directory, "credential")
	writeProtectedFixture(t, path, []byte("synthetic"), 0600)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	metadata := *info.Sys().(*syscall.Stat_t)
	for _, special := range []os.FileMode{os.ModeSetuid, os.ModeSetgid, os.ModeSticky} {
		if trustedFile(metadataFileInfo{FileInfo: info, metadata: metadata, mode: 0600 | special}, false) ||
			trustedFile(metadataFileInfo{FileInfo: info, metadata: metadata, mode: 0755 | special}, true) {
			t.Fatal("special file permission bits accepted")
		}
	}
	metadata.Uid = uint32(os.Getuid() + 1)
	if trustedFile(metadataFileInfo{FileInfo: info, metadata: metadata}, false) {
		t.Fatal("other-owner credential accepted")
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Uid = 0
	if !trustedFile(metadataFileInfo{FileInfo: info, metadata: metadata}, true) {
		t.Fatal("root-owned protected executable rejected")
	}
	metadata.Uid = uint32(os.Getuid() + 1)
	if trustedFile(metadataFileInfo{FileInfo: info, metadata: metadata}, true) {
		t.Fatal("other-owner executable accepted")
	}
}

func TestProtectedFIFOIsBounded(t *testing.T) {
	directory := protectedFixtureDirectory(t)
	path := filepath.Join(directory, "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-test.run=^TestProtectedFIFOHelper$")
	command.Env = []string{"MCPLAUNCH_TEST_FIFO=" + path}
	if err := command.Run(); err != nil {
		t.Fatal("FIFO rejection did not terminate successfully within its bound")
	}
}

func TestProtectedFIFOHelper(t *testing.T) {
	path := os.Getenv("MCPLAUNCH_TEST_FIFO")
	if path == "" {
		return
	}
	if _, err := readProtected(path, 64); !errors.Is(err, errProtected) {
		t.Fatal("FIFO was not rejected")
	}
}

func TestPrepareProtectedConfigAndCredential(t *testing.T) {
	directory := protectedFixtureDirectory(t)
	fields := validConfigFields()
	fields["binary_path"] = filepath.Join(directory, "ahe-query-mcp")
	fields["database_dns_file"] = filepath.Join(directory, "database.dsn")
	configPath := filepath.Join(directory, "launcher.json")
	writeProtectedFixture(t, fields["binary_path"], []byte("synthetic executable fixture"), 0700)
	writeProtectedFixture(t, fields["database_dns_file"], []byte("postgres://ahe_query_login:synthetic-password@localhost/ahe_test?sslmode=require"), 0600)
	writeProtectedFixture(t, configPath, encodeConfig(t, fields), 0600)
	cfg, credential, err := prepare(configPath)
	if err != nil || cfg.PrincipalID != fields["principal_id"] || credential == "" {
		t.Fatal("protected launcher fixture rejected")
	}
	var output bytes.Buffer
	if err := Run([]string{"--config", configPath}, nil, &output); !errors.Is(err, errExec) || output.Len() != 0 || strings.Contains(err.Error(), "synthetic-password") {
		t.Fatal("invalid executable format did not produce a fixed safe exec failure")
	}
	writeProtectedFixture(t, fields["database_dns_file"], []byte("postgres://other:secret-marker@localhost/ahe_test?sslmode=require"), 0600)
	if _, _, err := prepare(configPath); !errors.Is(err, errCredential) || strings.Contains(err.Error(), "secret-marker") {
		t.Fatal("wrong credential identity did not yield a safe failure")
	}
	if err := os.Chmod(configPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepare(configPath); !errors.Is(err, errProtected) {
		t.Fatal("unprotected launcher config accepted")
	}
}

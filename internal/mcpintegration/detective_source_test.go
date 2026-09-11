package mcpintegration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// detectiveSharedSourceRoot validates the explicit monorepo coordinate without
// connecting to PostgreSQL or starting a model, launcher, or build.
func detectiveSharedSourceRoot(root string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("AHE_DETECTIVE_SOURCE_ROOT must be an explicit clean absolute AHE MCP checkout path")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return "", errors.New("AHE_DETECTIVE_SOURCE_ROOT must use its existing canonical checkout path")
	}
	modulePath := filepath.Join(root, "go.mod")
	info, err := os.Lstat(modulePath)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("selected checkout has no regular root Go module")
	}
	module, err := os.ReadFile(modulePath)
	if err != nil || !strings.HasPrefix(string(module), "module github.com/Yui-Qi-Tang/ahe-mcp\n") {
		return "", errors.New("selected checkout does not contain the shared AHE MCP Go module")
	}
	for _, directory := range []string{"apps", "apps/detective", "apps/detective/cmd", "apps/detective/cmd/detective"} {
		if _, err := os.Lstat(filepath.Join(root, directory, "go.mod")); !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("selected Detective source must use only the shared root Go module")
		}
	}
	mainPath := filepath.Join(root, "apps", "detective", "cmd", "detective", "main.go")
	canonical, err = filepath.EvalSymlinks(mainPath)
	if err != nil || canonical != mainPath {
		return "", errors.New("selected checkout has no canonical Detective CLI source")
	}
	info, err = os.Lstat(mainPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("selected checkout has no regular Detective CLI source")
	}
	return root, nil
}

func TestDetectiveSharedSourceRoot(t *testing.T) {
	for _, name := range []string{"shared_root", "empty", "relative", "unclean", "symlink_root", "missing_module", "wrong_module", "legacy_module", "symlink_module", "missing_cli", "directory_cli", "symlink_cli", "nested_module", "app_root"} {
		t.Run(name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(base, "mcp")
			mainPath := filepath.Join(root, "apps", "detective", "cmd", "detective", "main.go")
			if err := os.MkdirAll(filepath.Dir(mainPath), 0o700); err != nil {
				t.Fatal(err)
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			remove := func(path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			link := func(target, path string) {
				t.Helper()
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			modulePath := filepath.Join(root, "go.mod")
			write(modulePath, "module github.com/Yui-Qi-Tang/ahe-mcp\n\ngo 1.27.0\n")
			write(mainPath, "package main\nfunc main() {}\n")
			selected := root
			switch name {
			case "empty":
				selected = ""
			case "relative":
				selected = "mcp"
			case "unclean":
				selected = root + "/."
			case "symlink_root":
				selected = filepath.Join(base, "alias")
				link(root, selected)
			case "missing_module":
				remove(modulePath)
			case "wrong_module":
				write(modulePath, "module example.invalid/unrelated\n")
			case "legacy_module":
				write(modulePath, "module yuki.tang/agents/detective\n")
			case "symlink_module":
				remove(modulePath)
				path := filepath.Join(base, "module.txt")
				write(path, "module github.com/Yui-Qi-Tang/ahe-mcp\n")
				link(path, modulePath)
			case "missing_cli":
				remove(mainPath)
			case "directory_cli":
				remove(mainPath)
				if err := os.Mkdir(mainPath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink_cli":
				remove(mainPath)
				path := filepath.Join(base, "main.go")
				write(path, "package main\n")
				link(path, mainPath)
			case "nested_module":
				write(filepath.Join(root, "apps", "detective", "go.mod"), "module example.invalid/nested\n")
			case "app_root":
				selected = filepath.Join(root, "apps", "detective")
			}
			got, err := detectiveSharedSourceRoot(selected)
			if name == "shared_root" {
				if err != nil || got != root {
					t.Fatalf("valid shared checkout: root=%q err=%v", got, err)
				}
			} else if err == nil || got != "" {
				t.Fatalf("invalid checkout accepted: root=%q err=%v", got, err)
			}
		})
	}
}

//go:build integration

package mcpintegration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Private artifacts stay under the checkout that compiled this test. Never
// substitute a caller's working directory or accept a configurable DB root.
// Trimmed or unavailable source coordinates fail closed before any DB access.
func multisurfaceLabBin(t *testing.T) string {
	t.Helper()
	root := hanScaleRoot(t)
	canonical, err := filepath.EvalSymlinks(root)
	if !filepath.IsAbs(root) || err != nil || canonical != root {
		t.Fatal("cannot identify canonical multisurface test checkout")
	}
	return filepath.Join(root, "bin")
}

func multisurfaceOriginalPlanPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(multisurfaceLabBin(t), "han-outcome-lab.oTBR2P", "plan-v1.json")
}

func multisurfaceReportCoordinate(path, bin, prefix string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		filepath.Dir(filepath.Dir(path)) != bin ||
		!strings.HasPrefix(filepath.Base(filepath.Dir(path)), prefix) {
		return false
	}
	root := filepath.Dir(path)
	info, err := os.Lstat(root)
	canonical, canonicalErr := filepath.EvalSymlinks(root)
	return err == nil && canonicalErr == nil && info.IsDir() &&
		info.Mode().Perm() == 0o700 && canonical == root
}

func TestMultisurfacePathsUseCompiledCheckout(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	bin := filepath.Join(root, "bin")
	if multisurfaceLabBin(t) != bin ||
		multisurfaceOriginalPlanPath(t) != filepath.Join(bin, "han-outcome-lab.oTBR2P", "plan-v1.json") ||
		practicalAnchorPGData(t) != filepath.Join(bin, "multisurface-lab.pi2AGw", "pgdata") {
		t.Fatal("checkout relocation changed a pinned artifact or database coordinate")
	}
}

func TestMultisurfaceReportCoordinateRejectsEscapes(t *testing.T) {
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"multisurface-lab.", "practical-han-lab."} {
		t.Run(prefix, func(t *testing.T) {
			root := filepath.Join(bin, prefix+"synthetic")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "report.json")
			if !multisurfaceReportCoordinate(path, bin, prefix) {
				t.Fatal("canonical private report coordinate was rejected")
			}
			alias := filepath.Join(bin, prefix+"alias")
			if err := os.Symlink(root, alias); err != nil {
				t.Fatal(err)
			}
			for _, rejected := range []string{
				filepath.Join(alias, "report.json"),
				root + "/../" + filepath.Base(root) + "/report.json",
				filepath.Join(root, "child", "report.json"),
				filepath.Join(filepath.Dir(bin), prefix+"outside", "report.json"),
				filepath.Join(bin, "wrong-prefix", "report.json"),
				filepath.Join(bin, prefix+"missing", "report.json"),
				"relative/report.json", "",
			} {
				if multisurfaceReportCoordinate(rejected, bin, prefix) {
					t.Error("escaped or unavailable report coordinate was accepted")
				}
			}
			if err := os.Chmod(root, 0o750); err != nil {
				t.Fatal(err)
			}
			if multisurfaceReportCoordinate(path, bin, prefix) {
				t.Fatal("nonprivate report directory was accepted")
			}
		})
	}
}

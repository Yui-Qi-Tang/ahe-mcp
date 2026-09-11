package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The retained proxy can run with an arbitrary working directory. Bind its lab
// to the checkout that compiled this test binary, not the caller's directory or
// an old developer-specific path. A trimmed or unavailable source path refuses
// to enable the lab instead of guessing another checkout.
func briefRecoveryBuildDirectory() (string, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(source) {
		return "", errors.New("recovery test source coordinate unavailable")
	}
	return filepath.EvalSymlinks(filepath.Join(filepath.Dir(source), "..", "..", "build"))
}

func briefRecoveryLabCoordinate(lab, buildDir string) bool {
	if !filepath.IsAbs(lab) || filepath.Clean(lab) != lab || filepath.Dir(lab) != buildDir ||
		!strings.HasPrefix(filepath.Base(lab), "brief-desktop-lab.") {
		return false
	}
	canonical, err := filepath.EvalSymlinks(lab)
	return err == nil && canonical == lab
}

func TestBriefRecoveryBuildDirectoryUsesCurrentCheckout(t *testing.T) {
	want, err := filepath.Abs("../../build")
	if err != nil {
		t.Fatal(err)
	}
	want, err = filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	// The proxy executable is not required to launch from the package directory.
	t.Chdir(t.TempDir())
	got, err := briefRecoveryBuildDirectory()
	if err != nil || got != want {
		t.Fatalf("recovery build coordinate = %q, %v; want %q", got, err, want)
	}
}

func TestBriefRecoveryLabCoordinateRejectsEscapes(t *testing.T) {
	buildDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lab := filepath.Join(buildDir, "brief-desktop-lab.synthetic")
	if err := os.Mkdir(lab, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(buildDir, "brief-desktop-lab.alias")
	if err := os.Symlink(lab, alias); err != nil {
		t.Fatal(err)
	}
	if !briefRecoveryLabCoordinate(lab, buildDir) {
		t.Fatal("current checkout's exact synthetic lab coordinate was rejected")
	}
	for _, path := range []string{
		alias, lab + "/../" + filepath.Base(lab), lab + "/child",
		filepath.Join(buildDir, "brief-desktop-lab.missing"),
		filepath.Join(filepath.Dir(buildDir), "brief-desktop-lab.outside"),
		"brief-desktop-lab.relative", "/old-checkout/build/brief-desktop-lab.old",
	} {
		if briefRecoveryLabCoordinate(path, buildDir) {
			t.Errorf("accepted an escaped or unavailable recovery coordinate: %q", path)
		}
	}
}

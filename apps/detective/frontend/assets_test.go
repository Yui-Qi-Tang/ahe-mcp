package frontend

import (
	"path"
	"regexp"
	"strings"
	"testing"
)

func TestAssetsContainProductionEntryPoint(t *testing.T) {
	body, err := Assets.ReadFile("dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `id="root"`) || strings.Contains(text, "/src/main.jsx") {
		t.Fatal("desktop assets must contain the built React entry point")
	}
	refs := regexp.MustCompile(`(?:src|href)="([^"]+)"`).FindAllStringSubmatch(text, -1)
	if len(refs) < 2 {
		t.Fatal("desktop entry point must reference its bundled script and stylesheet")
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref[1], "./assets/") || strings.Contains(ref[1], "..") {
			t.Fatalf("unexpected asset reference %q", ref[1])
		}
		asset, err := Assets.ReadFile(path.Join("dist", strings.TrimPrefix(ref[1], "./")))
		if err != nil || len(asset) == 0 {
			t.Fatalf("missing embedded asset %q", ref[1])
		}
	}
}

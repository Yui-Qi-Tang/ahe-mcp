package desktop

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

// NewSourceCollector opens a fresh private workspace for an explicit CLI source
// operation. Existing directories are rejected atomically, so the CLI cannot
// reopen Desktop settings. This performs no network, model or AHE operation.
// The caller must Close the returned service and retain files on failure.
func NewSourceCollector(dataDir string, connection Connection) (*Service, error) {
	if err := sourcemcp.Validate(sourceConfig(connection)); err != nil {
		return nil, errors.New("invalid source collector configuration")
	}
	settings := initialSettings()
	settings.Mode = "local"
	settings.Connections = []Connection{connection}
	if _, err := validateSettings(settings, false); err != nil {
		return nil, errors.New("invalid source collector configuration")
	}
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir || dataDir == string(filepath.Separator) || !safeText(dataDir, 4096) {
		return nil, errors.New("source collector requires a new clean absolute directory")
	}
	if err := checkDirectoryPath(filepath.Dir(dataDir), false); err != nil {
		return nil, errors.New("source collector parent must exist without symlinks")
	}
	// Do not replace this with Stat followed by New: New can reopen saved state.
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		return nil, errors.New("source collector directory must not already exist")
	}
	s, err := New(dataDir)
	if err != nil {
		return nil, errors.New("cannot open new private source collector directory; retain it for inspection")
	}
	// Other defaults remain inert; this source-only entry point exposes neither
	// model nor AHE commands, and never imports endpoints from the environment.
	if _, err := s.SaveSettings(settings); err != nil {
		s.Close()
		return nil, errors.New("cannot save source collector settings; retain the new directory for inspection")
	}
	return s, nil
}

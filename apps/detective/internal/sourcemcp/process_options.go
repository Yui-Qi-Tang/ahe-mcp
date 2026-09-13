package sourcemcp

import (
	"errors"
	"path/filepath"
	"runtime"
)

// ValidateProcessOptions validates literal argv and cwd, never a shell command.
// Directory is a working directory, not a filesystem permission boundary.
func ValidateProcessOptions(c Config) error {
	if c.CodebaseCache != "" && (runtime.GOOS != "darwin" || c.Transport != "stdio" || c.Directory == "" || len(c.Args) != 0 || !filepath.IsAbs(c.CodebaseCache) || filepath.Clean(c.CodebaseCache) != c.CodebaseCache || !validText(c.CodebaseCache, 4096)) {
		return errors.New("invalid offline codebase preset")
	}
	if c.Transport != "stdio" && (len(c.Args) != 0 || c.Directory != "") {
		return errors.New("process options require stdio")
	}
	if len(c.Args) > 32 {
		return errors.New("too many source process arguments")
	}
	bytes := 0
	for _, arg := range c.Args {
		if !validText(arg, 4096) {
			return errors.New("invalid source process argument")
		}
		bytes += len(arg)
	}
	if bytes > 16<<10 {
		return errors.New("source process arguments exceed bound")
	}
	if c.Directory != "" && (!filepath.IsAbs(c.Directory) || filepath.Clean(c.Directory) != c.Directory || !validText(c.Directory, 4096)) {
		return errors.New("source working directory must be a clean absolute path")
	}
	return nil
}

package desktop

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestNewSourceCollectorRequiresFreshPrivateDirectory(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection := Connection{ID: "source-test", Name: "Synthetic source", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_status"}}
	directory := filepath.Join(parent, "capture")
	s, err := NewSourceCollector(directory, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state := s.Snapshot()
	if state.Settings.Mode != "local" || !reflect.DeepEqual(state.Settings.Connections, []Connection{connection}) || state.Settings.IntakeLauncher != "" || state.Settings.QueryLauncher != "" || state.Source != nil || len(state.Tools) != 0 || len(state.Messages) != 0 {
		t.Fatal("collector loaded unrelated state or started an external operation")
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("collector directory is not private")
	}
	path := filepath.Join(directory, "settings.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("collector settings are not private")
	}
	connection.AllowedTools[0] = "changed"
	if s.Snapshot().Settings.Connections[0].AllowedTools[0] != "read_status" {
		t.Fatal("collector retained caller-owned policy slice")
	}
	if other, err := NewSourceCollector(directory, connection); err == nil {
		other.Close()
		t.Fatal("collector reopened existing settings")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected collector modified existing settings")
	}
}

func TestNewSourceCollectorRejectsInvalidConfigurationBeforeCreatingDirectory(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*Connection)
	}{
		{"invalid-id", func(c *Connection) { c.ID = "invalid id" }},
		{"credential-url", func(c *Connection) { c.URL = "http://secret@127.0.0.1:1/mcp" }},
		{"unicode-control-name", func(c *Connection) { c.Name = "test\u0085name" }},
		{"too-many-tools", func(c *Connection) {
			for i := range 33 {
				c.AllowedTools = append(c.AllowedTools, "read_"+strconv.Itoa(i))
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := Connection{ID: "source-test", Name: "Synthetic source", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_status"}}
			test.change(&connection)
			directory := filepath.Join(parent, test.name)
			if s, err := NewSourceCollector(directory, connection); err == nil {
				s.Close()
				t.Fatal("invalid configuration accepted")
			}
			if _, err := os.Lstat(directory); !os.IsNotExist(err) {
				t.Fatal("invalid configuration created output directory")
			}
		})
	}
}

func TestNewSourceCollectorRejectsSymlinkParent(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}
	connection := Connection{ID: "source-test", Name: "Synthetic source", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_status"}}
	if s, err := NewSourceCollector(filepath.Join(link, "capture"), connection); err == nil {
		s.Close()
		t.Fatal("collector followed symlink parent")
	}
	if _, err := os.Stat(filepath.Join(parent, "capture")); !os.IsNotExist(err) {
		t.Fatal("collector wrote through symlink parent")
	}
}

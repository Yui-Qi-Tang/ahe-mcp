package mcplaunch

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

const (
	maxConfigBytes     = 16 * 1024
	maxCredentialBytes = 16 * 1024
)

type config struct {
	SchemaVersion   string
	BinaryPath      string
	DatabaseDNSFile string
	Database        string
	SessionUser     string
	Schema          string
	Role            string
	Profile         string
	PrincipalID     string
	RepositoryRoot  string
	RepositoryID    string
}

func parseConfig(body []byte) (config, error) {
	var cfg config
	if len(body) == 0 || len(body) > maxConfigBytes || !utf8.Valid(body) {
		return cfg, errConfig
	}
	fields := map[string]*string{
		"schema_version": &cfg.SchemaVersion, "binary_path": &cfg.BinaryPath,
		"database_dns_file": &cfg.DatabaseDNSFile, "database": &cfg.Database,
		"session_user": &cfg.SessionUser, "schema": &cfg.Schema,
		"role": &cfg.Role, "profile": &cfg.Profile, "principal_id": &cfg.PrincipalID,
		"repository_root": &cfg.RepositoryRoot, "repository_id": &cfg.RepositoryID,
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return config{}, errConfig
	}
	seen := make(map[string]bool, len(fields))
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		target, known := fields[key]
		if err != nil || !ok || !known || seen[key] {
			return config{}, errConfig
		}
		valueToken, err := decoder.Token()
		value, ok := valueToken.(string)
		if err != nil || !ok {
			return config{}, errConfig
		}
		*target = value
		seen[key] = true
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return config{}, errConfig
	}
	if _, err := decoder.Token(); err != io.EOF {
		return config{}, errConfig
	}
	for key := range fields {
		if key != "repository_root" && key != "repository_id" && !seen[key] {
			return config{}, errConfig
		}
	}
	if cfg.Profile == "repository-intake" {
		if !seen["repository_root"] || !seen["repository_id"] || !absolutePath(cfg.RepositoryRoot) ||
			cfg.RepositoryID == "" || len(cfg.RepositoryID) > 200 || !utf8.ValidString(cfg.RepositoryID) ||
			strings.TrimSpace(cfg.RepositoryID) != cfg.RepositoryID || strings.ContainsFunc(cfg.RepositoryID, unicode.IsControl) {
			return config{}, errConfig
		}
	} else if seen["repository_root"] || seen["repository_id"] {
		return config{}, errConfig
	}
	if cfg.SchemaVersion != "ahe-mcp-launcher/v1" || !absolutePath(cfg.BinaryPath) || !absolutePath(cfg.DatabaseDNSFile) ||
		!identifier(cfg.Database) || !identifier(cfg.SessionUser) || !identifier(cfg.Schema) || !identifier(cfg.Role) {
		return config{}, errConfig
	}
	if _, err := runtimeauth.NewPrincipal(cfg.PrincipalID); err != nil {
		return config{}, errConfig
	}
	if strings.EqualFold(cfg.Schema, "public") || strings.EqualFold(cfg.Schema, "information_schema") ||
		strings.HasPrefix(strings.ToLower(cfg.Schema), "pg_") || strings.HasPrefix(strings.ToLower(cfg.Role), "pg_") || cfg.Role == cfg.SessionUser {
		return config{}, errConfig
	}
	var binaryName string
	switch cfg.Profile {
	case "query":
		binaryName = "ahe-query-mcp"
	case "intake", "source-claim-reviewer", "relation-reviewer", "endpoint-reviewer", "repository-intake":
		binaryName = "ahe-ingest-mcp"
	default:
		return config{}, errConfig
	}
	if filepath.Base(cfg.BinaryPath) != binaryName {
		return config{}, errConfig
	}
	return cfg, nil
}

func absolutePath(path string) bool {
	return path != "" && path != string(filepath.Separator) && len(path) <= 4096 && filepath.IsAbs(path) &&
		filepath.Clean(path) == path && utf8.ValidString(path) && !strings.ContainsFunc(path, unicode.IsControl)
}

func identifier(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for i := range len(value) {
		letter := value[i] >= 'a' && value[i] <= 'z' || value[i] >= 'A' && value[i] <= 'Z'
		if !letter && value[i] != '_' && !(i > 0 && value[i] >= '0' && value[i] <= '9') {
			return false
		}
	}
	return true
}

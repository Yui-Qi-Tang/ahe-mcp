// Package mcplaunch launches one operator-selected MCP process from protected
// local configuration. It does not authenticate human review or verify DB ACLs.
package mcplaunch

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

var (
	errArguments   = errors.New("usage: ahe-mcp-launch --config /absolute/protected/config.json")
	errConfig      = errors.New("invalid launcher configuration")
	errProtected   = errors.New("launcher requires protected regular files and trusted parent directories")
	errCredential  = errors.New("invalid launcher database credential or target binding")
	errUnsupported = errors.New("launcher is supported only on Linux and macOS")
	errExec        = errors.New("could not execute the configured MCP binary")
)

// Run accepts only an explicit protected config path or standalone help. On
// Linux and macOS success replaces this process, preserving PID and stdio.
// inheritedEnv is filtered, never changed, and supplies no authority fields.
func Run(args []string, inheritedEnv []string, stdout io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(stdout, "Usage: ahe-mcp-launch --config /absolute/protected/config.json\n\nLaunch one protected query, intake, or source-claim-reviewer configuration.\nCredentials are read from its protected database_dns_file, never CLI arguments.")
		return err
	}
	if len(args) != 2 || args[0] != "--config" {
		return errArguments
	}
	cfg, credential, err := prepare(args[1])
	if err != nil {
		return err
	}
	return replaceProcess(cfg.BinaryPath, childEnvironment(inheritedEnv, cfg, credential))
}

func prepare(configPath string) (config, string, error) {
	body, err := readProtected(configPath, maxConfigBytes)
	if err != nil {
		return config{}, "", err
	}
	cfg, err := parseConfig(body)
	if err != nil {
		return config{}, "", err
	}
	if err := checkExecutable(cfg.BinaryPath); err != nil {
		return config{}, "", err
	}
	credentialBody, err := readProtected(cfg.DatabaseDNSFile, maxCredentialBytes)
	if err != nil {
		return config{}, "", err
	}
	credential, err := databaseURL(credentialBody, cfg)
	if err != nil {
		return config{}, "", err
	}
	return cfg, credential, nil
}

func childEnvironment(inherited []string, cfg config, credential string) []string {
	values := map[string]string{"PATH": "/usr/bin:/bin:/usr/sbin:/sbin"}
	for _, entry := range inherited {
		key, value, found := strings.Cut(entry, "=")
		if !found || strings.ContainsRune(value, 0) {
			continue
		}
		switch key {
		case "HOME", "USER", "LOGNAME", "TMPDIR", "TZ", "LANG", "LC_ALL", "LC_CTYPE", "TERM":
			values[key] = value
		}
	}
	values["DATABASE_DSN"] = credential
	values["AHE_RUNTIME_PRINCIPAL_ID"] = cfg.PrincipalID
	values["AHE_RUNTIME_PROFILE"] = cfg.Profile
	values["AHE_DATABASE_ROLE"] = cfg.Role
	values["AHE_DATABASE_SCHEMA"] = cfg.Schema
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

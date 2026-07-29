//go:build darwin

package macos

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAHEDetectiveWrapperInjectsCredentialWithoutMigration(t *testing.T) {
	fixture := wrapperFixture(t, 0o600)
	command := exec.Command("/bin/sh", "run-ahe-detective.sh")
	command.Env = append(os.Environ(),
		"AHE_DETECTIVE_BINARY="+fixture.binary,
		"AHE_DETECTIVE_CONFIG="+fixture.config,
		"AHE_DATABASE_DNS_FILE="+fixture.credential,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper error = %v\noutput:\n%s", err, output)
	}
	if got, want := strings.TrimSpace(string(output)), "--config "+fixture.config; got != want {
		t.Fatalf("wrapper args = %q, want %q", got, want)
	}
}

func TestRunAHEDetectiveWrapperRejectsLooseCredentialMode(t *testing.T) {
	fixture := wrapperFixture(t, 0o644)
	command := exec.Command("/bin/sh", "run-ahe-detective.sh")
	command.Env = append(os.Environ(),
		"AHE_DETECTIVE_BINARY="+fixture.binary,
		"AHE_DETECTIVE_CONFIG="+fixture.config,
		"AHE_DATABASE_DNS_FILE="+fixture.credential,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("wrapper accepted loose credential mode:\n%s", output)
	}
	if !strings.Contains(string(output), "mode must be 0400 or 0600") {
		t.Fatalf("wrapper output = %q", output)
	}
}

type detectiveWrapperFixture struct {
	binary     string
	config     string
	credential string
}

func wrapperFixture(t *testing.T, credentialMode os.FileMode) detectiveWrapperFixture {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "ahe-detective")
	config := filepath.Join(root, "detective.json")
	credential := filepath.Join(root, "database-dns")
	fakeBinary := `#!/bin/sh
if [ "$DATABASE_DNS" != "postgres://wrapper-test" ]; then
	exit 9
fi
printf '%s\n' "$*"
`
	if err := os.WriteFile(binary, []byte(fakeBinary), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(credential, []byte("postgres://wrapper-test\n"), credentialMode); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	return detectiveWrapperFixture{binary: binary, config: config, credential: credential}
}

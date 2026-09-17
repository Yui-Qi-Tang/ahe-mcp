package mcplaunch

import (
	"strings"
	"testing"
)

func TestRepositoryLauncherFixedScope(t *testing.T) {
	fields := validConfigFields()
	fields["profile"] = "repository-intake"
	fields["binary_path"] = "/trusted/ahe-ingest-mcp"
	if _, err := parseConfig(encodeConfig(t, fields)); err == nil {
		t.Fatal("missing repository scope accepted")
	}
	fields["repository_root"] = "/synthetic/repository"
	fields["repository_id"] = "synthetic:repo"
	cfg, err := parseConfig(encodeConfig(t, fields))
	if err != nil {
		t.Fatal(err)
	}
	env := childEnvironment([]string{"AHE_REPOSITORY_ROOT=/unapproved", "AHE_REPOSITORY_ID=unapproved", "GIT_DIR=/unapproved", "GIT_CONFIG_COUNT=1"}, cfg, "synthetic-credential")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "unapproved") || !strings.Contains(joined, "AHE_REPOSITORY_ROOT=/synthetic/repository") {
		t.Fatal("inherited repository authority crossed launcher")
	}
	for _, profile := range []string{"query", "intake", "source-claim-reviewer", "relation-reviewer", "endpoint-reviewer"} {
		fields["profile"] = profile
		if _, err := parseConfig(encodeConfig(t, fields)); err == nil {
			t.Fatal("repository config entered other profile")
		}
	}
}

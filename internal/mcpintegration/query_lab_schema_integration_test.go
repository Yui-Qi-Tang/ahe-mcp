//go:build integration

package mcpintegration

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Query replays use the current binary and policy. Frozen evidence and query
// expectations remain fixed, but the restored database must be upgraded by a
// separate operator before this read-only verification starts.
func queryLabVerifyCurrent(ctx context.Context, pool *pgxpool.Pool, schema string, tables map[string]hanQueryLabTableState) error {
	if _, err := migrations.VerifyCurrentInSchema(ctx, pool, schema); err != nil {
		return fmt.Errorf("query replay requires operator migration before read-only verification: %w", err)
	}
	return queryLabValidateTables(slices.Collect(maps.Keys(tables)))
}

func queryLabValidateTables(names []string) error {
	manifest, err := dbrole.BuildManifest(dbrole.ProfileQuery)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(manifest.Tables))
	for _, table := range manifest.Tables {
		want[table.Table] = true
	}
	var unexpected, duplicate []string
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			duplicate = append(duplicate, name)
		}
		seen[name] = true
		if !want[name] {
			unexpected = append(unexpected, name)
		}
	}
	var missing []string
	for name := range want {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	if len(missing)+len(unexpected)+len(duplicate) > 0 {
		slices.Sort(missing)
		slices.Sort(unexpected)
		slices.Sort(duplicate)
		return fmt.Errorf("query lab table inventory mismatch: missing=%v unexpected=%v duplicate=%v", missing, unexpected, duplicate)
	}
	return nil
}

func TestQueryLabTableInventory(t *testing.T) {
	manifest, err := dbrole.BuildManifest(dbrole.ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	var current []string
	for _, table := range manifest.Tables {
		current = append(current, table.Table)
	}
	wrongName := slices.Clone(current)
	wrongName[0] = "unexpected_replacement_table"
	for _, tc := range []struct {
		name   string
		tables []string
		valid  bool
	}{
		{"current", current, true},
		{"missing", current[1:], false},
		{"extra", append(slices.Clone(current), "unexpected_table"), false},
		{"same_count_wrong_table", wrongName, false},
		{"duplicate", append(slices.Clone(current), current[0]), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := queryLabValidateTables(tc.tables); (err == nil) != tc.valid {
				t.Fatalf("valid=%t, error=%v", tc.valid, err)
			}
		})
	}
}

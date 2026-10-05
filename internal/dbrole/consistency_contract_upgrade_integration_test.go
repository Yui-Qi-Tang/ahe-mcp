//go:build integration

package dbrole

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationConsistencyContractUpgradeRetainsRoles(t *testing.T) {
	raw, err := os.ReadFile("testdata/upgrade53_baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if roleUpgradeHash(raw) != "53f24d52d1b2a4c3f62d978e053c93336c1a755cde5968c531cffbcb88868a0c" {
		t.Fatal("frozen role baseline changed")
	}
	var baseline roleUpgradeBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.SourceCommit != "2cf9476528aaba4edab92f0a857e5c1a605a4f79" || len(baseline.MigrationChecksums) != 53 {
		t.Fatal("unexpected baseline")
	}
	ctx, _, pool, actors := roleUpgradeFixture(t, baseline)
	before := roleUpgradeIdentities(t, ctx, pool, actors)
	in := evidenceingestion.ConsistencyWatchInput{WatchID: "preserved-watch", RequestID: "preserved-config", EngineID: "fixed-external-engine", Request: evidenceingestion.ConsistencyRequest{Scope: evidenceingestion.ConsistencyScope{Namespace: "fixed", ScopeRef: "scope"}, RuleVersion: "1"}}
	if _, err := evidenceingestion.RegisterConsistencyWatch(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	old, err := evidenceingestion.ReadConsistencyWatch(ctx, pool, in.WatchID, -1)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := migrations.ApplyUp(ctx, pool); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if _, err := migrations.VerifyCurrent(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, roleUpgradeIdentities(t, ctx, pool, actors)) {
		t.Fatal("upgrade changed existing identities")
	}
	for _, actor := range actors {
		if actor.Group == "" {
			login, err := pgxpool.NewWithConfig(ctx, actor.Config.Copy())
			if err != nil {
				t.Fatal("open isolated unscoped login failed")
			}
			_, err = login.Exec(ctx, `SELECT body FROM evidence.consistency_watch_versions LIMIT 1`)
			login.Close()
			roleUpgradePermissionDenied(t, err)
			continue
		}
		runtime, _, err := OpenRuntimePool(ctx, actor.Config, RuntimePoolInput{Role: actor.Group, Schema: "evidence", Profile: actor.Profile})
		if err != nil {
			t.Fatal(err)
		}
		got, err := evidenceingestion.ReadConsistencyWatch(ctx, runtime, in.WatchID, -1)
		if err != nil || !reflect.DeepEqual(got, old) {
			runtime.Close()
			t.Fatal("history changed for existing user", actor.Label, err)
		}
		next := in
		next.WatchID = "new-" + actor.Label
		next.RequestID = "new-" + actor.Label
		_, err = evidenceingestion.RegisterConsistencyWatch(ctx, runtime, next)
		runtime.Close()
		if actor.Profile == ProfileCoreRecords {
			if err != nil {
				t.Fatal("writer lost authority", err)
			}
		} else {
			roleUpgradePermissionDenied(t, err)
		}
	}
}

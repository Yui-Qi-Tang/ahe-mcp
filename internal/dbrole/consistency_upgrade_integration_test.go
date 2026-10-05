//go:build integration

package dbrole

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
)

func TestIntegrationConsistencyRoleUpgrade52(t *testing.T) {
	raw, err := os.ReadFile("testdata/upgrade52_baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if roleUpgradeHash(raw) != "ef542aec7c935e8613cc62c737e6bbaad7a51c816e1cd3e7af6d9b3dbade7154" {
		t.Fatal("frozen baseline changed")
	}
	var baseline roleUpgradeBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline.MigrationChecksums) != 52 || len(baseline.Manifests) != 7 || baseline.SourceCommit != "38d90f5c7d4cc0a57c788f74866e5462e1075b95" {
		t.Fatal("unexpected baseline")
	}
	ctx, owner, pool, actors := roleUpgradeFixture(t, baseline)
	identities := roleUpgradeIdentities(t, ctx, pool, actors)
	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.VerifyCurrent(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, actor := range actors {
		if actor.Group == "" || actor.Label == "query_b" {
			continue
		}
		roleUpgradeRejectOldPolicy(t, ctx, actor, baseline.Manifests[actor.Profile])
		if _, err := UpgradeRuntime(ctx, owner, ProvisionInput{Database: actor.Config.ConnConfig.Database, Role: actor.Group, SessionUser: actor.Login, Schema: "evidence", Profile: actor.Profile}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(identities, roleUpgradeIdentities(t, ctx, pool, actors)) {
		t.Fatal("roles recreated or changed during upgrade")
	}
	in := evidenceingestion.ConsistencyWatchInput{WatchID: "upgrade-watch", RequestID: "upgrade-config", EngineID: "externally-configured-engine", Request: evidenceingestion.ConsistencyRequest{Scope: evidenceingestion.ConsistencyScope{Namespace: "upgrade", ScopeRef: "fixed"}, RuleVersion: "1"}}
	for _, actor := range actors {
		if actor.Profile != ProfileCoreRecords {
			continue
		}
		runtime, _, err := OpenRuntimePool(ctx, actor.Config, RuntimePoolInput{Role: actor.Group, Schema: "evidence", Profile: actor.Profile})
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		if _, err := evidenceingestion.RegisterConsistencyWatch(ctx, runtime, in); err != nil {
			t.Fatal(err)
		}
		if _, err := evidenceingestion.RegisterConsistencyWatch(ctx, runtime, in); err != nil {
			t.Fatal("core replay", err)
		}
		if _, err := runtime.Exec(ctx, `UPDATE consistency_watches SET watch_id=watch_id`); err == nil {
			t.Fatal("history mutable")
		} else {
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
				t.Fatal("expected immutable-history guard", err)
			}
		}
		_, err = runtime.Exec(ctx, `INSERT INTO canonical_graph_nodes(canonical_node_id) VALUES('unauthorized')`)
		roleUpgradePermissionDenied(t, err)
	}
	var first evidenceingestion.ConsistencyEvents
	for _, actor := range actors {
		if actor.Profile != ProfileQuery {
			continue
		}
		runtime, _, err := OpenRuntimePool(ctx, actor.Config, RuntimePoolInput{Role: actor.Group, Schema: "evidence", Profile: actor.Profile})
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		events, err := evidenceingestion.ReadConsistencyEvents(ctx, runtime, in.WatchID, 0, 10)
		if err != nil || len(events.Events) != 1 {
			t.Fatal(events, err)
		}
		if actor.Label == "query_a" {
			first = events
		} else if !reflect.DeepEqual(first, events) {
			t.Fatal("same scope differs by user")
		}
		_, err = runtime.Exec(ctx, `INSERT INTO consistency_watches(watch_id) VALUES('query-write')`)
		roleUpgradePermissionDenied(t, err)
	}
	// RLS cannot silently turn hidden notifications into an empty complete read.
	if _, err := pool.Exec(ctx, `ALTER TABLE consistency_events ENABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	for _, actor := range actors {
		if actor.Label != "query_a" {
			continue
		}
		// OpenRuntimePool validates ACLs, and the read boundary rejects hidden rows.
		runtime, _, err := OpenRuntimePool(ctx, actor.Config, RuntimePoolInput{Role: actor.Group, Schema: "evidence", Profile: actor.Profile})
		if err == nil {
			defer runtime.Close()
			if _, err = evidenceingestion.ReadConsistencyEvents(ctx, runtime, in.WatchID, 0, 10); err == nil {
				t.Fatal("RLS empty result accepted")
			}
		}
	}
}

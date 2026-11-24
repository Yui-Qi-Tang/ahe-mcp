package evidencequerymcp

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
)

func TestQueryContractPublishesBinaryVersions(t *testing.T) {
	s := newServer(&fakeQueryCore{})
	raw, err := s.CallTool(context.Background(), ToolGetQueryContract, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var got QueryContract
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	latest, err := migrations.LatestMigration()
	if err != nil {
		t.Fatal(err)
	}
	if got.Contract != QueryContractV1 || !slices.Equal(got.ProjectionVersions, []string{evidenceuse.ProjectionV2}) ||
		!slices.Equal(got.UsePolicies, []string{string(evidenceuse.CurrentActionV1), string(evidenceuse.GroundedActionV1)}) ||
		got.ExpectedMigration != latest || got.RolePolicy != dbrole.PolicyVersion || len(got.Limitations) == 0 {
		t.Fatalf("contract = %+v", got)
	}
	again, err := s.CallTool(context.Background(), ToolGetQueryContract, []byte(`{}`))
	if err != nil || string(again) != string(raw) {
		t.Fatal("contract bytes are not stable for pinning", err)
	}
	if _, err := s.CallTool(context.Background(), ToolGetQueryContract, []byte(`{"version":"v1"}`)); err == nil {
		t.Fatal("contract tool accepted an unknown field")
	}
}

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/golden response samples")

// TestGoldenResponseSamples keeps exact sample bytes that downstream tests can
// parse and pin. A diff here is a downstream change: record it in CHANGELOG.md
// under Downstream impact before rewriting the files with -update-golden.
func TestGoldenResponseSamples(t *testing.T) {
	samples := governanceSampleResponses(t)
	contract, err := newServer(&fakeQueryCore{}).CallTool(context.Background(), ToolGetQueryContract, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	samples[ToolGetQueryContract] = contract
	for name, got := range samples {
		path := filepath.Join("testdata", "golden", name+".json")
		if *updateGolden {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run go test ./internal/evidencequerymcp -run TestGoldenResponseSamples -update-golden)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s response bytes changed; add a Downstream impact entry, then rerun with -update-golden\n got: %s\nwant: %s", name, got, want)
		}
	}
}

// consumerContract records what one downstream parser reads. Each file under
// testdata/consumer_contracts is owned by that consumer; this test fails when
// an AHE change would break the parser it describes.
type consumerContract struct {
	Consumer                   string   `json:"consumer"`
	AcceptedProjectionVersions []string `json:"accepted_projection_versions"`
	Outcomes                   []string `json:"outcomes"`
	StateDigestPrefix          string   `json:"state_digest_prefix"`
	Tools                      map[string]struct {
		RequestFields []string `json:"request_fields"`
		ResponsePaths []string `json:"response_paths"`
	} `json:"tools"`
}

func TestConsumerContracts(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "consumer_contracts", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no consumer contracts: %v", err)
	}
	responses := governanceSampleResponses(t)
	schemas := map[string]map[string]any{}
	for _, tool := range GovernanceTools() {
		schemas[tool.Name] = tool.InputSchema
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var c consumerContract
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for name, want := range c.Tools {
			t.Run(c.Consumer+"/"+name, func(t *testing.T) {
				schema, ok := schemas[name]
				if !ok {
					t.Fatalf("tool %s is no longer exposed", name)
				}
				properties := schema["properties"].(map[string]any)
				for _, field := range want.RequestFields {
					if _, ok := properties[field]; !ok {
						t.Errorf("request field %s is no longer accepted", field)
					}
				}
				for _, field := range schema["required"].([]string) {
					if !slices.Contains(want.RequestFields, field) {
						t.Errorf("new required field %s is not sent by %s", field, c.Consumer)
					}
				}
				var doc any
				if err := json.Unmarshal(responses[name], &doc); err != nil {
					t.Fatal(err)
				}
				for _, path := range want.ResponsePaths {
					if len(jsonPath(doc, strings.Split(path, "."))) == 0 {
						t.Errorf("response path %s is missing", path)
					}
				}
				for _, v := range jsonPath(doc, []string{"assessment", "projection_version"}) {
					if !slices.Contains(c.AcceptedProjectionVersions, v.(string)) {
						t.Errorf("projection %v is not accepted by %s", v, c.Consumer)
					}
				}
				for _, v := range jsonPath(doc, []string{"assessment", "state_digest"}) {
					if !strings.HasPrefix(v.(string), c.StateDigestPrefix) {
						t.Errorf("state digest %v lost prefix %s", v, c.StateDigestPrefix)
					}
				}
				for _, v := range jsonPath(doc, []string{"assessment", "nodes[]", "outcome"}) {
					if !slices.Contains(c.Outcomes, v.(string)) {
						t.Errorf("outcome %v is unknown to %s", v, c.Consumer)
					}
				}
			})
		}
	}
}

// governanceSampleResponses encodes the same types the governance tools return,
// built without a database from one cut whose root is absent (so it blocks).
func governanceSampleResponses(t *testing.T) map[string][]byte {
	t.Helper()
	snapshot := evidenceuse.Snapshot{Version: evidenceuse.ProjectionV2, StoreID: "store:sample", CutID: "cut:sample",
		Nodes: map[string]evidenceuse.Node{}, Sources: map[string]evidenceuse.Source{}, Contradictions: []evidenceuse.Contradiction{}}
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	assessment, err := evidenceuse.Assess(snapshot, evidenceuse.CurrentActionV1, []string{"canon-node:a"}, now)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := evidenceuse.CheckPlan(snapshot, evidenceuse.CurrentActionV1, []evidenceuse.ActionBasis{{ActionID: "act", BasisIDs: []string{"canon-node:a"}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(evidenceingestion.EvidenceUseProjection{Snapshot: snapshot, Assessment: assessment})
	if err != nil {
		t.Fatal(err)
	}
	checked, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{ToolGetEvidenceUseState: state, ToolCheckCurrentPlan: checked}
}

// jsonPath returns every value at path; a segment ending in [] fans out over
// an array. An empty array yields nothing, so samples must be nonempty.
func jsonPath(v any, path []string) []any {
	if len(path) == 0 {
		return []any{v}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	key, each := strings.CutSuffix(path[0], "[]")
	next, ok := obj[key]
	if !ok {
		return nil
	}
	if !each {
		return jsonPath(next, path[1:])
	}
	items, ok := next.([]any)
	if !ok {
		return nil
	}
	var out []any
	for _, item := range items {
		out = append(out, jsonPath(item, path[1:])...)
	}
	return out
}

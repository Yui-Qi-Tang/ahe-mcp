package evidencequerymcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

func summaryProjection(t *testing.T, count int) *evidenceingestion.EvidenceUseProjection {
	t.Helper()
	s := evidenceuse.Snapshot{Version: evidenceuse.ProjectionV2, StoreID: "synthetic-store", CutID: "governance:1",
		Nodes: map[string]evidenceuse.Node{}, Sources: map[string]evidenceuse.Source{}, Contradictions: []evidenceuse.Contradiction{}}
	for i := range count {
		id, source := fmt.Sprintf("canon-node:%d", i), fmt.Sprintf("source:%d", i)
		s.Nodes[id] = evidenceuse.Node{Kind: evidencegraph.CanonicalSourceClaim, Admission: "admitted",
			Withdrawal:  evidenceuse.Withdrawal{Status: evidenceuse.WithdrawalNone, Complete: true},
			Currentness: evidenceuse.Currentness{Complete: true}, Temporal: evidencegraph.TemporalRecord{Status: "unknown"},
			Parents: []string{}, ParentsComplete: true, SourceIDs: []string{source}, SourcesComplete: true, ContradictionsComplete: true}
		s.Sources[source] = evidenceuse.Source{SnapshotID: source, ContentHash: "sha256:synthetic", ProviderRevision: "v1", AssessmentsComplete: true}
	}
	root := s.Nodes["canon-node:0"]
	root.Kind, root.UseClass, root.Parents = evidencegraph.CanonicalDerivedClaim, evidencegraph.DerivationHistoricalOnly, []string{"canon-node:1"}
	s.Nodes["canon-node:0"] = root
	a, err := evidenceuse.Assess(s, evidenceuse.CurrentActionV1, []string{"canon-node:0"}, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return &evidenceingestion.EvidenceUseProjection{Snapshot: s, Assessment: a}
}

func TestAssessmentOnlyRetainsFullAssessmentWithoutWholeStore(t *testing.T) {
	p := summaryProjection(t, 5588)
	full, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeUseState(p)
	compact, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(summary.Assessment, p.Assessment) || len(p.Assessment.Nodes) != 2 {
		t.Fatal("assessment roots/ancestry/reasons or full-state digest changed")
	}
	c := summary.SnapshotContext
	if c.NodeCount != 5588 || c.StoreID != p.Snapshot.StoreID || c.CutID != p.Snapshot.CutID ||
		!c.SnapshotOmitted || c.DigestRecomputableFromResponse || c.DigestScope != "complete_snapshot" || c.GlobalAbsenceInferenceAllowed {
		t.Fatal("omitted snapshot misrepresented", c)
	}
	if bytes.Contains(compact, []byte(`"snapshot":`)) || bytes.Contains(compact, []byte("canon-node:5587")) || len(compact) >= 8<<10 || len(full) <= 1<<20 {
		t.Fatal("unrelated store leaked or size regression", len(compact), len(full))
	}
	summary.Assessment.Roots[0] = "changed"
	summary.Assessment.Nodes[0].Reasons[0].Code = "changed"
	after, _ := json.Marshal(p)
	if !bytes.Equal(full, after) {
		t.Fatal("summary mutation reached original projection")
	}
	t.Logf("synthetic 5588-node full=%d bytes, assessment_only=%d bytes", len(full), len(compact))
}

func TestAssessmentOnlyRecordAndCachedViewKeepFullMode(t *testing.T) {
	p := summaryProjection(t, 3)
	core := &fakeQueryCore{result: evidenceingestion.ProposalQueryResult{ProposalOccurrenceID: "occ:synthetic", UseState: p},
		canonicalResult:     evidenceingestion.CanonicalQueryResult{CanonicalID: "canon-node:0", UseState: p},
		canonicalReadResult: evidenceingestion.CanonicalReadView{UseState: p, Artifact: queryCanonicalReadArtifact(t), RootNodeIDs: []string{"canon-node:raw"}, MaxDepth: 1, MaxNodes: 8, MaxEdges: 16}}
	s := newServer(core)
	for _, req := range []GetEvidenceRecordRequest{{ProposalOccurrenceID: "occ:synthetic"}, {CanonicalID: "canon-node:0"}} {
		before, err := s.GetEvidenceRecord(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		req.UseStateMode = UseStateAssessmentOnly
		compact, err := s.GetEvidenceRecord(t.Context(), req)
		if err != nil || compact.UseState != nil || compact.UseStateSummary == nil || !reflect.DeepEqual(compact.UseStateSummary.Assessment, p.Assessment) {
			t.Fatal("record summary", err)
		}
		req.UseStateMode = UseStateFull
		after, err := s.GetEvidenceRecord(t.Context(), req)
		if err != nil || !reflect.DeepEqual(before, after) || after.UseState != p || after.UseStateSummary != nil {
			t.Fatal("explicit full changed", err)
		}
	}
	ctx := queryOwnerContext(t, "summary-owner")
	req := OpenCanonicalReadViewRequest{RootNodeIDs: []string{"canon-node:raw"}, MaxDepth: 1, MaxNodes: 8, MaxEdges: 16}
	full, err := s.OpenCanonicalReadView(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.UseStateMode = UseStateAssessmentOnly
	compact, err := s.OpenCanonicalReadView(ctx, req)
	if err != nil || compact.UseState != nil || compact.UseStateSummary == nil || !reflect.DeepEqual(compact.View, full.View) {
		t.Fatal("view summary changed identity", err)
	}
	compact.UseStateSummary.Assessment.Roots[0] = "changed"
	cached, err := s.cachedCanonicalReadView(ctx, full.View.Handle)
	if err != nil || cached.view.UseState != p || p.Assessment.Roots[0] != "canon-node:0" {
		t.Fatal("summary changed cached full view", err)
	}
	req.UseStateMode = UseStateFull
	after, err := s.OpenCanonicalReadView(ctx, req)
	if err != nil || !reflect.DeepEqual(after, full) {
		t.Fatal("full after compact changed", err)
	}
}

func TestUseStateModesRejectBeforeDatabase(t *testing.T) {
	s := newServer(&fakeQueryCore{})
	for tool, base := range map[string]string{
		ToolGetEvidenceUseState:   `"canonical_ids":["canon-node:0"],"policy":"current-action/v1"`,
		ToolGetEvidenceRecord:     `"canonical_id":"canon-node:0"`,
		ToolOpenCanonicalReadView: `"root_node_ids":["canon-node:0"],"max_depth":0,"max_nodes":1,"max_edges":0`,
	} {
		for _, mode := range []string{`null`, `""`, `"compact"`, `" full "`, `1`, `{}`} {
			_, err := s.CallTool(t.Context(), tool, []byte("{"+base+`,"use_state_mode":`+mode+"}"))
			if err == nil || !strings.Contains(err.Error(), "use_state_mode") {
				t.Fatal(tool, mode, err)
			}
		}
	}
}

//go:build integration && labreplay

package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const searchLab02PGFixtureHash = "sha256:70d3d42a353a7a0a093bd7266ce6f7d0a36f0b3474dab7573339eaea44b0f8d3"

func TestIntegrationSearchLab02BodyPGOneRow(t *testing.T) {
	ctx, pool, _ := searchLab02PGPool(t)
	source, _ := searchLab02PGSeed(t, ctx, pool, "one-row", "v1", searchLab02PGTexts(t)[0], []ExtractorProposalOutput{
		{ProposalLocalID: "one", StatementText: "The service team inspected the terminal cover.", EvidenceRefs: []string{"span:S1"}},
	})
	got, err := searchLab02PGBody(ctx, pgxDB{pool: pool}, fullLabSearchInput{SourceSnapshotID: source.SourceSnapshotID}, "source_body", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	searchLab02PGCheck(t, ctx, pool, got, []string{"one"}, "source_body")
}

type searchLab02PGMeasurement struct {
	CaseID string                  `json:"case_id"`
	Result searchLab02PGBodyResult `json:"result"`
}

// The prior source bytes are reused, not its synthetic outcome/eligibility or
// partial-ref contracts. Native proposals cite whole catalog spans only.
func TestIntegrationSearchLab02BodyPG(t *testing.T) {
	ctx, pool, cleanup := searchLab02PGPool(t)
	texts := searchLab02PGTexts(t)
	source, records := searchLab02PGSeed(t, ctx, pool, "main", "v1", texts[0], []ExtractorProposalOutput{
		{ProposalLocalID: "p01", StatementText: "A replacement cover is mentioned in the team's summary of the morning inspection.", EvidenceRefs: []string{"span:S1"}},
		{ProposalLocalID: "p02", StatementText: "The stored module requires the supervisor's release signature.", EvidenceRefs: []string{"span:S2"}},
		{ProposalLocalID: "p03", StatementText: "The service team inspected the terminal cover before the morning shift.", EvidenceRefs: []string{"span:S1"}},
		{ProposalLocalID: "p04", StatementText: "The afternoon crew is assigned the corridor cleaning rota.", EvidenceRefs: []string{"span:S4"}},
	})
	// Validate every declared span against the frozen complete line, rather
	// than assuming that a synthetic span ID identifies arbitrary bytes.
	lines := strings.Split(texts[0], "\n")
	if len(source.Spans) != len(lines) {
		t.Fatal("native catalog differs from frozen complete lines")
	}
	for i, span := range source.Spans {
		if span.SpanID != fmt.Sprintf("span:S%d", i+1) || span.QuotedText != lines[i] {
			t.Fatal("native catalog identity or original bytes differ")
		}
	}
	foreign, _ := searchLab02PGSeed(t, ctx, pool, "foreign", "v1", texts[1], []ExtractorProposalOutput{
		{ProposalLocalID: "foreign", StatementText: "The archive team stores a replacement cartridge.", EvidenceRefs: []string{"span:S1"}},
	})
	_, _ = searchLab02PGSeed(t, ctx, pool, "main", "v2", texts[0], []ExtractorProposalOutput{
		{ProposalLocalID: "v2", StatementText: "The team mentions a replacement module in another revision.", EvidenceRefs: []string{"span:S2"}},
	})
	searchLab02PGDispose(t, ctx, pool, records["p02"].ProposalOccurrenceID, ProposalDispositionAuditOnly)
	searchLab02PGDispose(t, ctx, pool, records["p04"].ProposalOccurrenceID, ProposalDispositionRejected)
	base := fullLabSearchInput{SourceSnapshotID: source.SourceSnapshotID, LifecycleScope: ProposalLifecycleScopeActive, Limit: 100}
	before := searchLab02PGState(t, ctx, pool)
	var measurements []searchLab02PGMeasurement
	for _, tc := range []struct {
		id, mode string
		filter   func(*fullLabSearchInput)
		want     []string
	}{
		{"statement", "statement", nil, []string{"p01"}},
		{"original_citations", "cited_refs", nil, []string{"p01", "p02"}},
		{"body_keeps_shared_source_noise", "source_body", nil, []string{"p01", "p02", "p03", "p04"}},
		{"pending_filter", "source_body", func(p *fullLabSearchInput) { p.AdmissionOutcome = "pending" }, []string{"p01", "p03"}},
		{"audit_only_filter", "source_body", func(p *fullLabSearchInput) { p.AdmissionOutcome = "audit_only" }, []string{"p02"}},
		{"rejected_filter", "source_body", func(p *fullLabSearchInput) { p.AdmissionOutcome = "rejected" }, []string{"p04"}},
		{"no_admitted_fixture", "source_body", func(p *fullLabSearchInput) { p.AdmissionOutcome = "admitted" }, nil},
		{"exact_source_id", "source_body", func(p *fullLabSearchInput) { p.SourceID = "lab02-main" }, []string{"p01", "p02", "p03", "p04"}},
		{"source_id_case_mismatch", "source_body", func(p *fullLabSearchInput) { p.SourceID = "LAB02-main" }, nil},
		{"exact_version", "source_body", func(p *fullLabSearchInput) { p.SourceVersion = "v1" }, []string{"p01", "p02", "p03", "p04"}},
		{"wrong_version", "source_body", func(p *fullLabSearchInput) { p.SourceVersion = "v2" }, nil},
		{"manual_all_scope", "source_body", func(p *fullLabSearchInput) { p.LifecycleScope = "all" }, []string{"p01", "p02", "p03", "p04"}},
		{"manual_is_not_repository_history", "source_body", func(p *fullLabSearchInput) { p.LifecycleScope = "historical" }, nil},
		{"foreign_snapshot_explicit", "source_body", func(p *fullLabSearchInput) { p.SourceSnapshotID = foreign.SourceSnapshotID }, []string{"foreign"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			input := base
			if tc.filter != nil {
				tc.filter(&input)
			}
			got, err := searchLab02PGBody(ctx, pgxDB{pool: pool}, input, tc.mode, 64, nil)
			if err != nil {
				t.Fatal(err)
			}
			searchLab02PGCheck(t, ctx, pool, got, tc.want, tc.mode)
			wantScoped := len(tc.want)
			if tc.mode != "source_body" {
				wantScoped = 4 // Lexical misses remain in the filtered candidate slice.
			}
			if got.ScopedRows != wantScoped {
				t.Fatalf("filtered candidate count %d, want %d", got.ScopedRows, wantScoped)
			}
			measurements = append(measurements, searchLab02PGMeasurement{tc.id, got})
		})
	}
	if t.Failed() {
		t.Fatal("matrix failed; no passing report will be produced")
	}
	t.Run("filter_before_output_limit", func(t *testing.T) {
		input := base
		input.AdmissionOutcome = "pending"
		got, err := searchLab02PGBody(ctx, pgxDB{pool: pool}, input, "source_body", 1, nil)
		if err != nil || got.Complete || got.ScopedRows != 2 || len(got.Hits) != 1 || len(got.OmittedIDs) != 1 {
			t.Fatalf("bounded filtered result differs: %v", err)
		}
		ids := []string{records["p01"].ProposalOccurrenceID, records["p03"].ProposalOccurrenceID}
		slices.Sort(ids)
		if got.Hits[0].OriginalProposal.ProposalOccurrenceID != ids[0] || got.OmittedIDs[0] != ids[1] {
			t.Fatal("output limit displaced a filtered-in candidate")
		}
		measurements = append(measurements, searchLab02PGMeasurement{"filter_before_output_limit", got})
	})
	t.Run("cross_view_binding_rejected", func(t *testing.T) {
		if _, err := LoadBoundedSourceView(ctx, pool, source.SourceSnapshotID, foreign.ExtractionViewID); err == nil {
			t.Fatal("foreign view was accepted for another source")
		}
	})
	t.Run("read_only_transaction_rejects_write", func(t *testing.T) {
		got, err := searchLab02PGBody(ctx, pgxDB{pool: pool}, base, "source_body", 64, func(tx sqlTx) error {
			_, err := tx.exec(ctx, "UPDATE proposal_occurrences SET statement_text = statement_text")
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "25006" || !reflect.DeepEqual(got, searchLab02PGBodyResult{}) {
			t.Fatal("read-only write did not fail closed with SQLSTATE 25006")
		}
	})
	if after := searchLab02PGState(t, ctx, pool); before != after {
		t.Fatal("read-only phase changed durable contents or canonical state")
	}

	// Commit a valid synthetic disposition between candidate listing and
	// source hydration, using another pool connection. No sleeps or mutation
	// of immutable evidence are needed to enforce this interleaving.
	var interleavings []map[string]string
	for _, isolation := range []string{"repeatable_read", "bare_read_committed_negative_control"} {
		id := records["p03"].ProposalOccurrenceID
		var db sqlDB = pgxDB{pool: pool}
		want := "pending"
		probeInput := base
		if isolation != "repeatable_read" {
			controlSource, added := searchLab02PGSeed(t, ctx, pool, "snapshot-control", "v1", texts[0], []ExtractorProposalOutput{
				{ProposalLocalID: "snapshot", StatementText: "The service team inspected the terminal cover.", EvidenceRefs: []string{"span:S1"}},
			})
			id = added["snapshot"].ProposalOccurrenceID
			probeInput.SourceSnapshotID = controlSource.SourceSnapshotID
			want = "rejected"
		}
		var observed string
		observeCommit := func(tx sqlTx) error {
			searchLab02PGDispose(t, ctx, pool, id, ProposalDispositionRejected)
			return tx.queryRow(ctx, "SELECT admission_outcome FROM proposal_occurrences WHERE proposal_occurrence_id=$1", id).Scan(&observed)
		}
		var got searchLab02PGBodyResult
		var err error
		if isolation == "repeatable_read" {
			got, err = searchLab02PGBody(ctx, db, probeInput, "source_body", 64, observeCommit)
		} else {
			// The real bridge rejects weak isolation before listing. This bare
			// transaction is a separate negative control, not a returned result.
			tx, beginErr := db.begin(ctx)
			if beginErr != nil {
				t.Fatal(beginErr)
			}
			defer tx.rollback(context.Background())
			if _, err = tx.exec(ctx, "SET TRANSACTION ISOLATION LEVEL READ COMMITTED, READ ONLY"); err != nil {
				t.Fatal(err)
			}
			var initial string
			if err = tx.queryRow(ctx, "SELECT admission_outcome FROM proposal_occurrences WHERE proposal_occurrence_id=$1", id).Scan(&initial); err != nil || initial != "pending" {
				t.Fatal("read committed control did not start pending")
			}
			err = observeCommit(tx)
			if rollbackErr := tx.rollback(context.Background()); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			got.Isolation = "read committed"
		}
		if err != nil || observed != want {
			t.Fatalf("snapshot interleaving %s got %s, want %s: %v", isolation, observed, want, err)
		}
		if isolation == "repeatable_read" {
			found := false
			for _, hit := range got.Hits {
				if hit.OriginalProposal.ProposalOccurrenceID == id {
					found = true
					if hit.OriginalProposal.AdmissionOutcome != "pending" {
						t.Fatal("same-snapshot result mixed post-commit state")
					}
				}
			}
			if !found {
				t.Fatal("controlled proposal disappeared from the original snapshot")
			}
		}
		fresh, err := GetProposalByOccurrenceID(ctx, pool, id)
		if err != nil || fresh.AdmissionOutcome != "rejected" {
			t.Fatal("fresh read did not observe committed disposition")
		}
		interleavings = append(interleavings, map[string]string{"control": isolation, "later_statement_outcome": observed, "fresh_outcome": fresh.AdmissionOutcome, "transaction_isolation": got.Isolation})
	}
	t.Run("weak_isolation_bridge_rejected", func(t *testing.T) {
		got, err := searchLab02PGBody(ctx, searchLab02PGReadCommittedDB{pgxDB{pool: pool}}, base, "source_body", 64, nil)
		var boundary *searchLab02PGBodyError
		if !errors.As(err, &boundary) || boundary.Code != "snapshot_contract" || !reflect.DeepEqual(got, searchLab02PGBodyResult{}) {
			t.Fatal("weak-isolation bridge did not reject the transaction")
		}
	})
	t.Run("unsupported_scope_does_not_expand", func(t *testing.T) {
		for _, change := range []func(*fullLabSearchInput){
			func(input *fullLabSearchInput) { input.SourceSnapshotID = "" },
			func(input *fullLabSearchInput) { input.RepositorySnapshotID = "repo-snapshot:outside" },
			func(input *fullLabSearchInput) { input.ExcludeCanonicalCandidates = true },
		} {
			input := base
			change(&input)
			got, err := searchLab02PGBody(ctx, pgxDB{pool: pool}, input, "source_body", 64, nil)
			var boundary *searchLab02PGBodyError
			if !errors.As(err, &boundary) || boundary.Code != "unsupported_request" || !reflect.DeepEqual(got, searchLab02PGBodyResult{}) {
				t.Fatal("unsupported scope or filter was silently ignored")
			}
		}
	})

	t.Run("oversized_body_is_not_empty_success", func(t *testing.T) {
		big, _ := searchLab02PGSeed(t, ctx, pool, "oversized", "v1", "The replacement record contains "+strings.Repeat("x", 8193)+".", []ExtractorProposalOutput{
			{ProposalLocalID: "big", StatementText: "The record concerns a replacement.", EvidenceRefs: []string{"span:S1"}},
		})
		input := base
		input.SourceSnapshotID = big.SourceSnapshotID
		got, err := searchLab02PGBody(ctx, pgxDB{pool: pool}, input, "source_body", 64, nil)
		var boundary *searchLab02PGBodyError
		if !errors.As(err, &boundary) || boundary.Code != "source_view_over_budget" || !reflect.DeepEqual(got, searchLab02PGBodyResult{}) {
			t.Fatal("over-budget source masqueraded as successful empty search")
		}
	})
	t.Run("candidate_cap_is_not_empty_success", func(t *testing.T) {
		var proposals []ExtractorProposalOutput
		for i := range 100 {
			proposals = append(proposals, ExtractorProposalOutput{ProposalLocalID: fmt.Sprintf("cap-%03d", i),
				StatementText: fmt.Sprintf("The team recorded inspection entry %d.", i), EvidenceRefs: []string{"span:S1"}})
		}
		capped, originals := searchLab02PGSeed(t, ctx, pool, "candidate-cap", "v1", texts[0], proposals)
		input := base
		input.SourceSnapshotID = capped.SourceSnapshotID
		got, err := searchLab02PGBody(ctx, pgxDB{pool: pool}, input, "source_body", 64, nil)
		var boundary *searchLab02PGBodyError
		if !errors.As(err, &boundary) || boundary.Code != "candidate_budget" || !reflect.DeepEqual(got, searchLab02PGBodyResult{}) {
			t.Fatal("candidate cap masqueraded as successful empty search")
		}
		searchLab02PGDispose(t, ctx, pool, originals["cap-000"].ProposalOccurrenceID, ProposalDispositionAuditOnly)
		input.AdmissionOutcome = "audit_only"
		got, err = searchLab02PGBody(ctx, pgxDB{pool: pool}, input, "source_body", 1, nil)
		if err != nil || got.ScopedRows != 1 {
			t.Fatalf("outcome filter was not applied before candidate cap: %v", err)
		}
		searchLab02PGCheck(t, ctx, pool, got, []string{"cap-000"}, "source_body")
		measurements = append(measurements, searchLab02PGMeasurement{"filter_before_candidate_cap", got})
	})
	t.Run("witness_limit_keeps_original_refs", func(t *testing.T) {
		var lines []string
		for i := range 9 {
			lines = append(lines, fmt.Sprintf("The replacement module remains in storage bay %d.", i+1))
		}
		wide, originals := searchLab02PGSeed(t, ctx, pool, "witness-cap", "v1", strings.Join(lines, "\n"), []ExtractorProposalOutput{
			{ProposalLocalID: "wide", StatementText: "The team recorded the storage bays.", EvidenceRefs: []string{"span:S1"}},
		})
		input := base
		input.SourceSnapshotID = wide.SourceSnapshotID
		got, err := searchLab02PGBody(ctx, pgxDB{pool: pool}, input, "source_body", 64, nil)
		if err != nil || got.Complete || len(got.Hits) != 1 || len(got.Hits[0].Witnesses) != 8 || !slices.Equal(got.Hits[0].OmittedSpanIDs, []string{"span:S9"}) || !reflect.DeepEqual(got.Hits[0].OriginalProposal, originals["wide"]) {
			t.Fatalf("witness cap changed native original references: %v", err)
		}
		for i, witness := range got.Hits[0].Witnesses {
			span := wide.Spans[i]
			expected := ResolvedSourceRef{ExtractionViewID: wide.ExtractionViewID, SpanID: span.SpanID,
				StartByte: span.StartByte, EndByte: span.EndByte, QuotedText: span.QuotedText, QuotedTextHash: span.QuotedTextHash}
			if witness.Ref != expected || witness.WithinOriginalRefs != (i == 0) || i == 0 && witness.Role != "original_citation" || i > 0 && witness.Role != "retrieval_context" {
				t.Fatal("limited body witness is not the exact native catalog span")
			}
		}
		measurements = append(measurements, searchLab02PGMeasurement{"witness_limit_keeps_original_refs", got})
	})
	if t.Failed() {
		t.Fatal("negative or interleaving controls failed; no passing report will be produced")
	}
	finalState := searchLab02PGState(t, ctx, pool)
	var canonical int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM canonical_graph_nodes)+(SELECT count(*) FROM canonical_graph_edges)").Scan(&canonical); err != nil || canonical != 0 {
		t.Fatal("synthetic experiment created canonical state")
	}
	var serverVersion string
	if err := pool.QueryRow(ctx, "SHOW server_version").Scan(&serverVersion); err != nil {
		t.Fatal("cannot read server version")
	}
	cleanup()
	if t.Failed() {
		t.Fatal("cleanup failed; no passing report will be produced")
	}
	report := map[string]any{
		"contract": "search-lab-02-native-pg-composition/v1", "fixture_sha256": searchLab02PGFixtureHash,
		"verification_revision": "native-witness-and-scoped-count-checks/v2",
		"source_base":           "5c2bc7c8866af4db07f3f73300b28418f6896468", "selected_source_hashes": searchLab02PGSourceHashes(t),
		"server_version": serverVersion, "measurements": measurements, "interleavings": interleavings,
		"read_only_phase_before_after_sha256": contentHash([]byte(before)), "final_synthetic_state_sha256": contentHash([]byte(finalState)),
		"canonical_rows": canonical, "schema_cleanup_verified": true,
		"passed_controls": []string{"cross_view_binding_rejected", "read_only_transaction_sqlstate_25006", "read_only_phase_selected_tables_byte_exact",
			"repeatable_read_retains_precommit_outcome", "fresh_read_observes_disposition", "bare_read_committed_observes_mixed_time",
			"weak_isolation_bridge_rejected", "unsupported_scope_rejected", "typed_source_view_over_budget", "typed_candidate_budget", "filter_before_candidate_cap", "witness_limit_preserves_original"},
		"limitations": []string{"Test-only composition of native list and bounded loader; no public search mode, runtime, migration, MCP or model change.",
			"Native source filters precede the 100-row candidate cap; at most 99 rows are accepted, not an unbounded corpus search.",
			"Manual active/all and historical-empty semantics only, not repository lifecycle switching or authenticated ACL proof.",
			"Frozen source text is reused; native proposals/outcomes are restated, not a replay of all seven offline cases.",
			"Partial references and synthetic eligibility are not native input fields; canonical-candidate/repository filters are not implemented by this bridge.",
			"Synthetic reject/audit_only setup and interleavings are test fixtures, not human review or admission; canonical state stays empty.",
			"Same-source unrelated proposals remain; these are contract checks, not improved search quality or semantic support."},
	}
	if path := os.Getenv("AHE_SEARCH_LAB_02_PG_REPORT"); path != "" {
		if !filepath.IsAbs(path) {
			t.Fatal("report output must be a new absolute path")
		}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal("report output unavailable or already exists")
		}
		_, writeErr := file.Write(append(encoded, '\n'))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("report write failed")
		}
	}
}

func searchLab02PGSeed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name, version, text string, proposals []ExtractorProposalOutput) (SourceIntakeResult, map[string]ProposalQueryResult) {
	t.Helper()
	source, err := CaptureManualSource(ctx, pool, ManualTextInput{SourceID: "lab02-" + name, SourceVersion: version, Raw: []byte(text), RequestID: "lab02-capture-" + name + "-" + version})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{RequestID: "lab02-extract-" + name + "-" + version,
		SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
		ExtractorDefinition: ExtractorDefinitionInput{Name: "lab-native-fixture", Version: "body-boundary/v1"}, Output: FrozenExtractorOutput{Proposals: proposals}}); err != nil {
		t.Fatal(err)
	}
	rows, err := fullLabListSearchRecords(ctx, pool, fullLabSearchInput{SourceSnapshotID: source.SourceSnapshotID, LifecycleScope: "all", Limit: 100})
	if err != nil || len(rows) != len(proposals) {
		t.Fatalf("native fixture cardinality differs: %v", err)
	}
	byLocal := make(map[string]ProposalQueryResult)
	for _, row := range rows {
		byLocal[row.ProposalLocalID] = row
	}
	return source, byLocal
}

func searchLab02PGDispose(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, outcome string) {
	t.Helper()
	if _, err := RecordPendingProposalDisposition(ctx, pool, ProposalDispositionInput{ProposalOccurrenceID: id, Outcome: outcome,
		DecisionBy: "synthetic-lab-fixture-not-human", DecisionReason: "Synthetic state for query isolation and filter controls, not a semantic decision."}); err != nil {
		t.Fatal(err)
	}
}

func searchLab02PGCheck(t *testing.T, ctx context.Context, pool *pgxpool.Pool, got searchLab02PGBodyResult, want []string, mode string) {
	t.Helper()
	var ids []string
	for _, hit := range got.Hits {
		ids = append(ids, hit.OriginalProposal.ProposalLocalID)
		original, err := GetProposalByOccurrenceID(ctx, pool, hit.OriginalProposal.ProposalOccurrenceID)
		if err != nil || !reflect.DeepEqual(original, hit.OriginalProposal) {
			t.Fatal("original native proposal changed")
		}
		var witnessIDs, expectedIDs []string
		loaded, err := LoadBoundedSourceView(ctx, pool, original.SourceSnapshotID, original.ExtractionViewID)
		if err != nil || loaded.Input == nil {
			t.Fatal("independent witness readback unavailable")
		}
		// Frozen fixture occurrences are all complete lowercase words. This
		// independent expected set is deliberately not a general tokenizer.
		if mode == "source_body" {
			for _, span := range loaded.Input.Spans {
				if strings.Contains(span.Text, "replacement") {
					expectedIDs = append(expectedIDs, span.SpanID)
				}
			}
		} else if mode == "cited_refs" {
			for _, ref := range original.SourceRefs {
				if strings.Contains(ref.QuotedText, "replacement") {
					expectedIDs = append(expectedIDs, ref.SpanID)
				}
			}
		}
		for _, witness := range hit.Witnesses {
			witnessIDs = append(witnessIDs, witness.Ref.SpanID)
			if witness.Ref.ExtractionViewID != original.ExtractionViewID || contentHash([]byte(witness.Ref.QuotedText)) != witness.Ref.QuotedTextHash {
				t.Fatal("source witness identity or bytes changed")
			}
			within := slices.Contains(original.SourceRefs, witness.Ref)
			if mode == "cited_refs" && !within || witness.WithinOriginalRefs != within || within && witness.Role != "original_citation" || !within && witness.Role != "retrieval_context" {
				t.Fatal("new source context masquerades as an original citation")
			}
			ref := witness.Ref
			if mode == "source_body" {
				index := slices.IndexFunc(loaded.Input.Spans, func(span ExtractorInputSpan) bool { return span.SpanID == ref.SpanID })
				if index < 0 {
					t.Fatal("body witness has no native catalog identity")
				}
				span := loaded.Input.Spans[index]
				expected := ResolvedSourceRef{ExtractionViewID: original.ExtractionViewID, SpanID: span.SpanID,
					StartByte: span.StartByte, EndByte: span.EndByte, QuotedText: span.Text, QuotedTextHash: span.QuotedTextHash}
				if ref != expected {
					t.Fatal("body witness is not the full native catalog span")
				}
			}
			if ref.StartByte < 0 || ref.EndByte > len(loaded.Input.RenderedText) || ref.StartByte >= ref.EndByte || loaded.Input.RenderedText[ref.StartByte:ref.EndByte] != ref.QuotedText {
				t.Fatal("witness offset differs from native rendered bytes")
			}
		}
		if !slices.Equal(witnessIDs, expectedIDs) || len(hit.OmittedSpanIDs) != 0 {
			t.Fatal("witness set lost or added a source span")
		}
	}
	slices.Sort(ids)
	if !slices.Equal(ids, want) || !got.Complete || got.Isolation != "repeatable read" || got.ReadOnly != "on" || got.Snapshot == "" {
		t.Fatalf("unexpected selection/snapshot: got %v want %v", ids, want)
	}
}

func searchLab02PGTexts(t *testing.T) []string {
	t.Helper()
	var fixture struct {
		Sources []struct {
			Text string `json:"text"`
		} `json:"sources"`
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("FULL_LAB_DATASET"), "search_lab_02_body_boundary_v1.json"))
	if err != nil || contentHash(raw) != searchLab02PGFixtureHash {
		t.Fatal("frozen source fixture changed or unavailable")
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.Sources) != 2 {
		t.Fatal("frozen source fixture shape differs")
	}
	return []string{fixture.Sources[0].Text, fixture.Sources[1].Text}
}

// Hash selected table row content, not counts alone; no raw DB identity or
// private connection data is retained. Each named table must remain exact.
func searchLab02PGState(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var state string
	for _, table := range []string{"source_blobs", "source_snapshots", "extraction_views", "span_catalog_entries", "extractor_definitions", "extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences", "admission_decisions", "canonical_graph_nodes", "canonical_graph_edges"} {
		var rows string
		if err := pool.QueryRow(ctx, "SELECT COALESCE(jsonb_agg(row_data ORDER BY row_data::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) row_data FROM "+table+" t) q").Scan(&rows); err != nil {
			t.Fatal("cannot read fixed lab state table: " + table)
		}
		state += table + "\n" + rows + "\n"
	}
	return state
}

func searchLab02PGSourceHashes(t *testing.T) map[string]string {
	t.Helper()
	hashes := make(map[string]string)
	for _, file := range []string{"search_lab_02_body_pg_probe_test.go", "search_lab_02_body_pg_environment_test.go", "search_lab_02_body_pg_integration_test.go", "postgres.go", "source_view_bounded.go", "proposal_disposition.go", "../../migrations/migrations.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal("selected source hash input unavailable")
		}
		hashes[file] = contentHash(raw)
	}
	return hashes
}

// Only this test adapter weakens isolation, proving that the controlled
// interleaving distinguishes READ COMMITTED from the real native RR contract.
type searchLab02PGReadCommittedDB struct{ sqlDB }
type searchLab02PGReadCommittedTx struct{ sqlTx }

func (db searchLab02PGReadCommittedDB) begin(ctx context.Context) (sqlTx, error) {
	tx, err := db.sqlDB.begin(ctx)
	if err != nil {
		return nil, err
	}
	return searchLab02PGReadCommittedTx{tx}, nil
}

func (tx searchLab02PGReadCommittedTx) exec(ctx context.Context, query string, args ...any) (execResult, error) {
	if query == "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY" {
		query = "SET TRANSACTION ISOLATION LEVEL READ COMMITTED, READ ONLY"
	}
	return tx.sqlTx.exec(ctx, query, args...)
}

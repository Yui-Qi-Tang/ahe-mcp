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
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type researchDispositionInput struct {
	Attempt  string
	Subject  ExactDisplayedReviewSubject
	Decision ProposalDispositionInput
}

func researchDisposition(ctx context.Context, pool *pgxpool.Pool, in researchDispositionInput, fail bool) (result ProposalDispositionResult, err error) {
	// Product derives the occurrence from the bound subject; do not repair an inconsistent original input.
	if in.Decision.ProposalOccurrenceID != in.Subject.ReviewSubject.ProposalOccurrenceID {
		return result, errors.New("subject/decision mismatch")
	}
	if fail {
		_, err = pool.Exec(ctx, `CREATE FUNCTION full_lab_disposition_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'FULL LAB DISPOSITION INJECTED'; END $$; CREATE CONSTRAINT TRIGGER full_lab_disposition_fault AFTER INSERT ON source_claim_disposition_review_bindings DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION full_lab_disposition_fault()`)
		if err != nil {
			return result, err
		}
		defer func() {
			_, e := pool.Exec(context.Background(), `DROP TRIGGER full_lab_disposition_fault ON source_claim_disposition_review_bindings; DROP FUNCTION full_lab_disposition_fault()`)
			err = errors.Join(err, e)
		}()
	}
	result, err = RecordReviewedSourceClaimDisposition(ctx, pool, ReviewedSourceClaimDispositionInput{
		ExtractionAttemptID: in.Attempt, ExpectedSubject: in.Subject, Outcome: in.Decision.Outcome,
		DecisionBy: in.Decision.DecisionBy, DecisionReason: in.Decision.DecisionReason})
	if fail && (err == nil || !strings.Contains(err.Error(), "FULL LAB DISPOSITION INJECTED")) {
		return result, fmt.Errorf("fault did not reach commit: %w", err)
	}
	return result, err
}

func researchOutput(t *testing.T, name string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	output := os.Getenv("AHE_RESEARCH_20261001_OUTPUT")
	if output == "" {
		output = filepath.Join("..", "..", "experiments", "research-20261001", "results")
	}
	path := filepath.Join(output, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}
func researchCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "source_claim_disposition_review_bindings"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func TestResearch20261001Disposition(t *testing.T) {
	ctx, pool, _ := fullLabReplayPool(t)
	checks := []string{}
	receipts := []ProposalDispositionResult{}
	makeInput := func(name, outcome string) researchDispositionInput {
		f := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, name, "revision-1")
		_, subject, err := LoadSourceClaimReviewDisplayArtifact(ctx, pool, f.ExtractionAttemptID, f.ProposalOccurrenceID)
		if err != nil {
			t.Fatal(err)
		}
		return researchDispositionInput{Attempt: f.ExtractionAttemptID, Subject: subject, Decision: ProposalDispositionInput{ProposalOccurrenceID: f.ProposalOccurrenceID, Outcome: outcome, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic research non-admission"}}
	}
	for i, outcome := range []string{ProposalDispositionRejected, ProposalDispositionAuditOnly} {
		in := makeInput(fmt.Sprintf("research-d-%d", i), outcome)
		// Every subject component and attempt is independently changed before writing.
		variations := []func(*researchDispositionInput){
			func(x *researchDispositionInput) { x.Subject.ReviewDisplayArtifactID += "x" },
			func(x *researchDispositionInput) { x.Subject.ReviewSubject.SubmissionReceiptID += "x" },
			func(x *researchDispositionInput) { x.Subject.ReviewSubject.ProposalManifestID += "x" },
			func(x *researchDispositionInput) { x.Subject.ReviewSubject.ProposalBasisID += "x" },
			func(x *researchDispositionInput) { x.Subject.ReviewSubject.ReviewPackageID += "x" },
			func(x *researchDispositionInput) { x.Subject.ReviewSubject.ProposalOccurrenceID += "x" },
			func(x *researchDispositionInput) { x.Attempt += "x" },
		}
		for j, change := range variations {
			bad := in
			change(&bad)
			before := researchCounts(t, ctx, pool)
			if _, err := researchDisposition(ctx, pool, bad, false); err == nil {
				t.Fatal("accepted changed subject")
			}
			if !reflect.DeepEqual(before, researchCounts(t, ctx, pool)) {
				t.Fatal("rejected subject wrote authority")
			}
			checks = append(checks, fmt.Sprintf("%s-subject-field-%d", outcome, j))
		}
		before := researchCounts(t, ctx, pool)
		if _, err := researchDisposition(ctx, pool, in, true); err == nil || !strings.Contains(err.Error(), "FULL LAB DISPOSITION INJECTED") {
			t.Fatalf("injection not reached: %v", err)
		}
		p, err := GetProposalByOccurrenceID(ctx, pool, in.Decision.ProposalOccurrenceID)
		if err != nil {
			t.Fatal(err)
		}
		if p.AdmissionOutcome != admissionOutcomePending || !reflect.DeepEqual(before, researchCounts(t, ctx, pool)) {
			t.Fatal("failed rollback")
		}
		checks = append(checks, outcome+"-rollback")
		first, err := researchDisposition(ctx, pool, in, false)
		if err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, first)
		before = researchCounts(t, ctx, pool)
		replay, err := researchDisposition(ctx, pool, in, false)
		if err != nil || !replay.Replayed || replay.AdmissionDecisionID != first.AdmissionDecisionID {
			t.Fatalf("replay: %+v %v", replay, err)
		}
		if !reflect.DeepEqual(before, researchCounts(t, ctx, pool)) {
			t.Fatal("replay wrote rows")
		}
		checks = append(checks, outcome+"-first-and-exact-replay")
		changes := []func(*researchDispositionInput){func(x *researchDispositionInput) { x.Decision.DecisionBy = "other" }, func(x *researchDispositionInput) { x.Decision.DecisionReason = "other" }, func(x *researchDispositionInput) {
			x.Decision.Outcome = ProposalDispositionRejected
			if outcome == ProposalDispositionRejected {
				x.Decision.Outcome = ProposalDispositionAuditOnly
			}
		}, func(x *researchDispositionInput) { x.Subject.ReviewDisplayArtifactID += "x" }}
		for j, change := range changes {
			bad := in
			change(&bad)
			if _, err := researchDisposition(ctx, pool, bad, false); err == nil {
				t.Fatal("conflicting replay accepted")
			}
			checks = append(checks, fmt.Sprintf("%s-replay-conflict-%d", outcome, j))
		}
	}
	legacy := makeInput("research-legacy", ProposalDispositionRejected)
	if _, err := RecordPendingProposalDisposition(ctx, pool, legacy.Decision); err != nil {
		t.Fatal(err)
	}
	if _, err := researchDisposition(ctx, pool, legacy, false); err == nil {
		t.Fatal("retrofitted legacy review")
	}
	checks = append(checks, "legacy-not-retrofitted")
	concurrent := makeInput("research-concurrent", ProposalDispositionRejected)
	other := concurrent
	other.Decision.DecisionReason = "competing synthetic decision"
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, in := range []researchDispositionInput{concurrent, other} {
		wg.Add(1)
		go func(in researchDispositionInput) {
			defer wg.Done()
			<-start
			_, err := researchDisposition(ctx, pool, in, false)
			errs <- err
		}(in)
	}
	close(start)
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("competing successes %d", success)
	}
	checks = append(checks, "conflicting-concurrent-only-one")
	counts := researchCounts(t, ctx, pool)
	if counts["canonical_graph_nodes"] != 0 || counts["canonical_graph_edges"] != 0 {
		t.Fatal("non-admission created graph")
	}
	researchOutput(t, "disposition.json", map[string]any{"checks": checks, "receipts": receipts, "counts": counts, "passed": true, "authority": "TEST APPROVAL STUB in disposable schema only", "scope": "native-backed Lab binding; not production writer, no proof of reading or provider latestness"})
}

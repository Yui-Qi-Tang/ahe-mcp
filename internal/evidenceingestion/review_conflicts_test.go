package evidenceingestion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConflictSuggestionsValidateBeforeOpeningTransaction(t *testing.T) {
	for _, req := range []ConflictSuggestionRequest{
		{}, {ProposalOccurrenceID: "occ:synthetic", Limit: 101}, {ProposalOccurrenceID: "occ:synthetic", ScanLimit: 1001},
	} {
		// An uninitialized pool must never be reached for malformed input.
		_, err := SuggestIntakeConflicts(t.Context(), &pgxpool.Pool{}, req)
		if err == nil {
			t.Fatal("invalid request reached database")
		}
		assertKind(t, err, ErrorInvalidInput)
	}
}

func TestReviewConflictBytesKeepWholeCandidatesAndCoverage(t *testing.T) {
	in := &ReviewConflictCandidates{Contract: ReviewConflictCandidatesV1, Status: "observed", TargetsComplete: true, Targets: []ReviewConflictTarget{}}
	for i := range 3 {
		target := ReviewConflictTarget{ProposalOccurrenceID: fmt.Sprintf("occ:%d", i), Coverage: ConflictSuggestionCoverage{ScanLimit: 1000, ScannedClaims: 50, MatchedClaims: 4, ReturnedClaims: 4}}
		for j := range 4 {
			target.Suggestions = append(target.Suggestions, ConflictSuggestion{CanonicalID: fmt.Sprintf("canon:%d:%d", i, j), Statement: strings.Repeat("\"<>&\n", 300)})
		}
		in.Targets = append(in.Targets, target)
	}
	// Include the encoded budget field itself in the exact-boundary witness.
	for {
		body, _ := json.Marshal(in)
		if in.MaxBytes == len(body) {
			break
		}
		in.MaxBytes = len(body)
	}
	before, _ := json.Marshal(in)
	exact, err := BoundReviewConflictCandidates(in, len(before))
	if err != nil || !reflect.DeepEqual(exact, in) {
		t.Fatal("exact budget changed advisory", err)
	}
	out, err := BoundReviewConflictCandidates(in, 10<<10)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(out)
	if len(encoded) > 10<<10 || !out.ByteBudgetTruncated || len(out.Targets) != 3 || !out.TargetsComplete {
		t.Fatal("byte or target coverage lost", len(encoded))
	}
	for i, target := range out.Targets {
		if target.Coverage.MatchedClaims != 4 || target.Coverage.ReturnedClaims != len(target.Suggestions) || target.ByteBudgetOmitted != 4-len(target.Suggestions) || !target.Coverage.ResultsTruncated {
			t.Fatal("omissions hidden", target)
		}
		if !reflect.DeepEqual(target.Suggestions, in.Targets[i].Suggestions[:len(target.Suggestions)]) {
			t.Fatal("candidate text or ranking prefix changed")
		}
	}
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("bounding mutated original observation")
	}
	if _, err := BoundReviewConflictCandidates(in, 1); err == nil {
		t.Fatal("unrepresentable metadata returned a partial success")
	}
}

func TestReviewConflictByteBudgetManyTargets(t *testing.T) {
	in := &ReviewConflictCandidates{Contract: ReviewConflictCandidatesV1, Status: "observed", TargetsComplete: true, MaxTargets: 65}
	for i := range 65 {
		target := ReviewConflictTarget{ProposalOccurrenceID: fmt.Sprintf("occ:%d", i), ReturnLimit: 20,
			Coverage: ConflictSuggestionCoverage{ScanLimit: 1000, MatchedClaims: 100, ReturnedClaims: 20, ResultsTruncated: true}}
		for j := range 20 {
			target.Suggestions = append(target.Suggestions, ConflictSuggestion{CanonicalID: fmt.Sprintf("canon:%d:%d", i, j), Statement: strings.Repeat("語意<&", 500)})
		}
		in.Targets = append(in.Targets, target)
	}
	out, err := BoundReviewConflictCandidates(in, ReviewConflictMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(out)
	if len(encoded) > ReviewConflictMaxBytes || len(out.Targets) != 65 || out.MaxBytes != ReviewConflictMaxBytes {
		t.Fatal("many-target bound", len(encoded))
	}
	for i, target := range out.Targets {
		if target.Coverage.ReturnedClaims+target.ByteBudgetOmitted != 20 || target.Coverage.MatchedClaims != 100 || !reflect.DeepEqual(target.Suggestions, in.Targets[i].Suggestions[:len(target.Suggestions)]) {
			t.Fatal("many-target coverage or source bytes changed")
		}
	}
}

func TestEndpointConflictTargetsIncludeTransitiveStatementOrigins(t *testing.T) {
	d := EndpointReviewDisplay{Request: EndpointReviewRequest{Kind: "derived_spec"}, Proposal: ProposalQueryResult{ProposalOccurrenceID: "occ:new"},
		Ancestors: []CanonicalQueryResult{
			{OriginProposalOccurrenceID: "occ:direct", OriginProposal: ProposalQueryResult{ProposalKind: ProposalKindStatement}},
			{OriginProposalOccurrenceID: "occ:transitive", OriginProposal: ProposalQueryResult{ProposalKind: ProposalKindStatement}},
		}}
	if got := endpointConflictTargets(d); !reflect.DeepEqual(got, []string{"occ:new", "occ:direct", "occ:transitive"}) {
		t.Fatal("ancestry target omitted", got)
	}
	d.Request.Kind = "repository_code"
	if got := endpointConflictTargets(d); len(got) != 0 {
		t.Fatal("code review treated as statement conflict scan")
	}
}

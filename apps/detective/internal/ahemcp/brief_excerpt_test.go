package ahemcp

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

func selectBriefSubmission(t *testing.T, original BriefSubmission, parent sourcepilot.BriefSource, start int, reason string) BriefSubmission {
	t.Helper()
	source, err := sourcepilot.SelectBriefExcerpt(parent, start, start+len(original.Report.Source.Body), reason)
	if err != nil {
		t.Fatal(err)
	}
	report := original.Report
	report.Source = source
	input, err := NewBriefSubmission(report, original.SourcePath, original.Model, original.Statement, original.Citation)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestBriefExcerptStorageIdentitySeparatesProvenanceForEqualBody(t *testing.T) {
	base := briefSubmissionFixture(t, "The service remains delayed.\nCause unknown.\n", 1, 1)
	parent := base.Report.Source
	parent.Coverage, parent.Limitations = "full_document", []string{}
	prefix := "Parent introduction.\n"
	parent.Body = prefix + base.Report.Source.Body + base.Report.Source.Body + "Original parent ending."
	first := selectBriefSubmission(t, base, parent, len(prefix), "Read the named incident.")
	firstID := briefSourceID(first)
	if !strings.HasPrefix(firstID, "brief-excerpt:") || firstID == parent.SourceID {
		t.Fatal("excerpt reused the declared parent source storage identity")
	}
	seen := map[string]bool{firstID: true}
	for _, name := range []string{"revision", "parent-body", "range", "reason", "source-url", "observation-time"} {
		t.Run(name, func(t *testing.T) {
			changed := parent
			start, reason := len(prefix), "Read the named incident."
			switch name {
			case "revision":
				changed.SourceRevision = "fixture-v2"
			case "parent-body":
				changed.Body += "Changed outside excerpt."
			case "range":
				start += len(base.Report.Source.Body)
			case "reason":
				reason = "Read the incident for a different stated reason."
			case "source-url":
				changed.SourceURL += "/other"
			case "observation-time":
				changed.ObservedAt = "2026-09-13T01:00:00Z"
			}
			input := selectBriefSubmission(t, base, changed, start, reason)
			id := briefSourceID(input)
			if input.Report.Source.SourceID != parent.SourceID || input.Report.Source.Body != first.Report.Source.Body || input.Report.BodySHA256 != first.Report.BodySHA256 || seen[id] {
				t.Fatal("equal excerpt bytes collided across distinct provenance or rewrote declared identity")
			}
			seen[id] = true
			raw, _ := json.Marshal(input)
			var retry BriefSubmission
			if json.Unmarshal(raw, &retry) != nil || ValidateBriefSubmission(retry) != nil || briefSourceID(retry) != id {
				t.Fatal("exact frozen retry changed source storage identity")
			}
		})
	}
}

func TestBriefExcerptPendingQueryAndReviewUseSameStorageIdentity(t *testing.T) {
	base, review, handoff, _ := briefReviewFixture(t)
	parent := base.Report.Source
	parent.Coverage, parent.Limitations = "full_document", []string{}
	prefix := "Parent introduction.\n"
	parent.Body = prefix + base.Report.Source.Body + "Parent ending."
	input := selectBriefSubmission(t, base, parent, len(prefix), "Read this exact source block.")
	input, review, _, query := briefReviewFixtureForInput(t, input, review, handoff)
	var firstSource, firstOutput []byte
	for attempt := range 2 {
		launcher, trace := handoffLauncher(t, "batch_pending")
		got, err := SubmitBrief(t.Context(), launcher, input)
		if err != nil {
			t.Fatalf("pending attempt %d: %v", attempt, err)
		}
		handoff = got
		calls := readHandoffTrace(t, trace)
		// Separate fake processes compare exact request bytes; they do not
		// simulate database idempotency or authenticate an earlier write.
		if !slices.Equal(handoffCallNames(calls), []string{"initialize", "notifications/initialized", "tools/list", "submit_text_source", "get_extractor_input", "submit_extractor_output"}) {
			t.Fatal("excerpt acquired an unexpected intake or reviewer write")
		}
		var sent sourceRequest
		if json.Unmarshal(calls[3].Arguments, &sent) != nil || sent.SourceID != briefSourceID(input) || sent.SourceVersion != contentHash(input.Report.Source.Body) || sent.OriginMetadata["declared_source_id"] != parent.SourceID || !reflect.DeepEqual(sent.OriginMetadata, briefOrigin(input)) {
			t.Fatal("storage identity diverged from frozen excerpt provenance")
		}
		if attempt == 0 {
			firstSource = append([]byte{}, calls[3].Arguments...)
			firstOutput = append([]byte{}, calls[5].Arguments...)
		} else if !bytes.Equal(firstSource, calls[3].Arguments) || !bytes.Equal(firstOutput, calls[5].Arguments) {
			t.Fatal("exact retry changed source or proposal request bytes")
		}
	}
	queryLauncher, queryTrace := reviewLauncher(t, "query", review, fixtureAdmission(handoff), &query, nil)
	if observed, err := ObserveBrief(t.Context(), queryLauncher, input, handoff); err != nil || observed.AdmissionOutcome != "pending" {
		t.Fatalf("pending Query did not recognize derived source identity: %v", err)
	}
	assertReviewOnlyCall(t, queryTrace, "get_evidence_record")
	reviewer, reviewTrace := reviewLauncher(t, "read", review, fixtureAdmission(handoff), nil, nil)
	if got, err := ReviewBrief(t.Context(), reviewer, input, handoff); err != nil || !reflect.DeepEqual(got, review) {
		t.Fatalf("native review lost source or origin binding: %v", err)
	}
	assertReviewOnlyCall(t, reviewTrace, "get_source_claim_review")
	wrong := query
	wrong.Source.SourceID = parent.SourceID
	if _, err := verifyBriefRecord(wrong, input, handoff); err == nil {
		t.Fatal("Query accepted the declared parent ID in place of excerpt storage ID")
	}
	var payload reviewPackage
	_ = json.Unmarshal([]byte(review.Display.PayloadUTF8), &payload)
	payload.ProposalBasis.OriginMetadataHash = "sha256:" + digest(briefOrigin(base))
	setReviewPayload(t, &review, payload)
	if ValidateBriefReview(review, input, handoff) == nil {
		t.Fatal("native review accepted first-writer origin from another source")
	}
}

func TestBriefSourceIdentityPreservesLegacyAndNoExcerptRequests(t *testing.T) {
	base := briefSubmissionFixture(t, "Exact source body.\n", 1, 1)
	for _, legacy := range []bool{false, true} {
		input := base
		if legacy {
			input.Report.Source.Version, input.Report.Source.SourceKind = "detective-brief-source/v1", ""
			input.Digest = ""
			input.Digest = digest(input)
		}
		if err := ValidateBriefSubmission(input); err != nil {
			t.Fatal(err)
		}
		if briefSourceID(input) != input.Report.Source.SourceID {
			t.Fatal("no-excerpt source identity changed")
		}
		receipt := sourceResult{SourceSnapshotID: fixtureID("srcsnap:"), ExtractionViewID: fixtureID("view:")}
		request, err := briefRequest(input, receipt, []span{{SpanID: "span:S1", DisplayLine: 1}})
		if err != nil {
			t.Fatal(err)
		}
		want := requestID("brief-proposal", input.Report.Source.SourceID, input.Report.BodySHA256, digest(briefDefinition(input)), digest(request.ExtractorOutput.Proposals))
		if request.RequestID != want || request.SourceSnapshotID != receipt.SourceSnapshotID || request.ExtractionViewID != receipt.ExtractionViewID {
			t.Fatal("existing request or receipt coordinates changed")
		}
		if legacy && len(briefOrigin(input)) != 8 {
			t.Fatal("legacy origin acquired new metadata")
		}
	}
}

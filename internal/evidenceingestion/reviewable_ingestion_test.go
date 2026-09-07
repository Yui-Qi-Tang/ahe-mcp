package evidenceingestion

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestReviewableIngestionManifestGoldenAndStable(t *testing.T) {
	batch := reviewableIngestionTestBatch(t, 1, "session:golden")
	manifest := mustReviewableIngestionManifest(t, batch)

	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal(manifest) error = %v", err)
	}
	const want = `{"contract_version":"reviewable-ingestion/v1","proposal_manifest_id":"proposal-manifest:v1:sha256:f566ec7bfd2dbfb5449416905572243f1b6fa47b8ca8b0475e096bed5117211f","proposal_batch_id":"batch:208ee17b9dec8d0eb811ab0d14117c473f3d3aa82a4a3d3707843864766298eb","extraction_attempt_id":"attempt:fb5211d91b98ad184603395408aef64d26054f33eccc19b07e7ca5858ba45947","extractor_output_hash":"sha256:816cd01556a2e75d4729b12a1e1d497ba733eec4df0bcca830a4a6f6a0f0d687","proposal_set_hash":"sha256:ee402ee515ccd468d41b16b139878e763e75c798dc60521ebb7c7f19cbc51591","proposal_count":1,"entries":[{"ordinal":1,"proposal_occurrence_id":"occ:4495a79e2c4933bbe5d9b92c09098459bce198beb4f8e7cc3a761ec030ffb50d","proposal_local_id":"proposal-000","proposal_fingerprint":"fp:895753c336b8c42b6c28c088723605ad6d69d47f7f8085ecd3263a4cab29aba8","proposal_fingerprint_version":"statement-v1","proposal_kind":"statement"}]}`
	if string(encoded) != want {
		t.Fatalf("manifest JSON changed:\n got: %s\nwant: %s", encoded, want)
	}

	rebuilt := mustReviewableIngestionManifest(t, batch)
	if !reflect.DeepEqual(rebuilt, manifest) {
		t.Fatalf("rebuilt manifest = %+v, want %+v", rebuilt, manifest)
	}

	mutatedBatch := batch
	mutatedBatch.Occurrences = append([]ProposalOccurrence(nil), batch.Occurrences...)
	mutatedBatch.Occurrences[0].ProposalLocalID = "caller-mutated"
	if manifest.Entries[0].ProposalLocalID == mutatedBatch.Occurrences[0].ProposalLocalID {
		t.Fatal("manifest entries alias caller-owned occurrences")
	}
}

func TestReviewableIngestionManifestCanonicalizesOccurrenceOrder(t *testing.T) {
	batch := reviewableIngestionTestBatch(t, 3, "")
	want := mustReviewableIngestionManifest(t, batch)

	reversed := batch
	reversed.Occurrences = append([]ProposalOccurrence(nil), batch.Occurrences...)
	slices.Reverse(reversed.Occurrences)
	got := mustReviewableIngestionManifest(t, reversed)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reordered occurrences changed manifest:\n got: %+v\nwant: %+v", got, want)
	}
	for index, entry := range got.Entries {
		if entry.Ordinal != index+1 || entry.ProposalLocalID != fmt.Sprintf("proposal-%03d", index) {
			t.Fatalf("entry %d = %+v, want canonical ordinal/local ID", index, entry)
		}
	}
}

func TestReviewableIngestionSubmissionReceiptSessionIdentityAndReplayConflict(t *testing.T) {
	firstBatch := reviewableIngestionTestBatch(t, 1, "session:first")
	firstManifest := mustReviewableIngestionManifest(t, firstBatch)
	first := mustReviewableIngestionReceipt(t, firstBatch, firstManifest)

	secondBatch := firstBatch
	secondBatch.ExtractionRun.ProducerSessionRef = "session:second"
	secondManifest := mustReviewableIngestionManifest(t, secondBatch)
	second := mustReviewableIngestionReceipt(t, secondBatch, secondManifest)

	if !reflect.DeepEqual(secondManifest, firstManifest) {
		t.Fatalf("session changed manifest:\n first: %+v\nsecond: %+v", firstManifest, secondManifest)
	}
	if first.ID != second.ID {
		t.Fatalf("session changed receipt identity: %q != %q", first.ID, second.ID)
	}
	if first.ProducerSessionRef == second.ProducerSessionRef || reflect.DeepEqual(first, second) {
		t.Fatalf("receipt bodies did not retain distinct session audit metadata: %+v / %+v", first, second)
	}
	if err := ValidateSubmissionReceiptReplay(first, first); err != nil {
		t.Fatalf("exact receipt replay error = %v", err)
	}
	assertKind(t, ValidateSubmissionReceiptReplay(first, second), ErrorReviewContractConflict)

	drifted := first
	drifted.ProposalCount++
	drifted.ID, _ = submissionReceiptID(drifted)
	assertKind(t, ValidateSubmissionReceiptReplay(first, drifted), ErrorReviewContractConflict)
}

func TestReviewableIngestionReviewPackageBindsExactSubjectAndDoesNotAlias(t *testing.T) {
	artifacts := newReviewableIngestionTestArtifacts(t, "session:package")
	want := cloneReviewPackage(artifacts.reviewPackage)
	if artifacts.reviewPackage.Limitations == nil || len(artifacts.reviewPackage.Limitations) == 0 {
		t.Fatalf("limitations = %#v, want explicit non-empty limitations", artifacts.reviewPackage.Limitations)
	}
	if err := ValidateExactSourceClaimReviewSubject(
		artifacts.receipt,
		artifacts.manifest,
		artifacts.current,
		artifacts.reviewPackage,
		artifacts.subject,
	); err != nil {
		t.Fatalf("ValidateExactSourceClaimReviewSubject(exact) error = %v", err)
	}

	artifacts.reviewPackage.ProposalBasis.SourceRefs[0].QuotedText = "caller-mutated"
	artifacts.reviewPackage.ProposalBasis.OriginMetadataHash = "caller-mutated"
	artifacts.reviewPackage.Limitations[0] = "caller-mutated"
	if artifacts.current.SourceRefs[0].QuotedText == "caller-mutated" {
		t.Fatal("review package aliases current proposal authority")
	}
	rebuilt, err := BuildSourceClaimReviewPackage(artifacts.receipt, artifacts.manifest, artifacts.current)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewPackage(rebuild) error = %v", err)
	}
	if !reflect.DeepEqual(rebuilt, want) {
		t.Fatalf("caller mutation changed rebuilt package:\n got: %+v\nwant: %+v", rebuilt, want)
	}

	packageTests := []struct {
		name   string
		mutate func(*ReviewPackage)
	}{
		{name: "package ID", mutate: func(candidate *ReviewPackage) { candidate.ID += "x" }},
		{name: "basis ID", mutate: func(candidate *ReviewPackage) { candidate.ProposalBasis.ID += "x" }},
		{name: "basis statement", mutate: func(candidate *ReviewPackage) { candidate.ProposalBasis.StatementText += " changed" }},
		{name: "basis source ref", mutate: func(candidate *ReviewPackage) { candidate.ProposalBasis.SourceRefs[0].StartByte++ }},
		{name: "basis origin metadata hash", mutate: func(candidate *ReviewPackage) { candidate.ProposalBasis.OriginMetadataHash += "x" }},
		{name: "basis source coverage", mutate: func(candidate *ReviewPackage) { candidate.ProposalBasis.SourceCoverage += "changed" }},
		{name: "nil basis source limitations", mutate: func(candidate *ReviewPackage) { candidate.ProposalBasis.SourceLimitations = nil }},
		{name: "manifest ordinal", mutate: func(candidate *ReviewPackage) { candidate.ProposalBasis.ProposalManifestOrdinal++ }},
		{name: "effect", mutate: func(candidate *ReviewPackage) { candidate.ProposedEffect += "-changed" }},
		{name: "coverage", mutate: func(candidate *ReviewPackage) { candidate.Coverage += " changed" }},
		{name: "nil limitations", mutate: func(candidate *ReviewPackage) { candidate.Limitations = nil }},
		{name: "changed limitation", mutate: func(candidate *ReviewPackage) { candidate.Limitations[0] += " changed" }},
	}
	for _, test := range packageTests {
		t.Run("package "+test.name, func(t *testing.T) {
			candidate := cloneReviewPackage(want)
			test.mutate(&candidate)
			assertKind(t, ValidateExactSourceClaimReviewSubject(
				artifacts.receipt,
				artifacts.manifest,
				artifacts.current,
				candidate,
				artifacts.subject,
			), ErrorReviewContractConflict)
		})
	}

	subjectTests := []struct {
		name   string
		mutate func(*ExactReviewSubject)
	}{
		{name: "receipt", mutate: func(subject *ExactReviewSubject) { subject.SubmissionReceiptID += "x" }},
		{name: "manifest", mutate: func(subject *ExactReviewSubject) { subject.ProposalManifestID += "x" }},
		{name: "occurrence", mutate: func(subject *ExactReviewSubject) { subject.ProposalOccurrenceID += "x" }},
		{name: "basis", mutate: func(subject *ExactReviewSubject) { subject.ProposalBasisID += "x" }},
		{name: "package", mutate: func(subject *ExactReviewSubject) { subject.ReviewPackageID += "x" }},
	}
	for _, test := range subjectTests {
		t.Run("subject "+test.name, func(t *testing.T) {
			candidate := artifacts.subject
			test.mutate(&candidate)
			assertKind(t, ValidateExactSourceClaimReviewSubject(
				artifacts.receipt,
				artifacts.manifest,
				artifacts.current,
				want,
				candidate,
			), ErrorReviewContractConflict)
		})
	}
}

func TestReviewableIngestionReviewPackageBindsExternalSourceCompleteness(t *testing.T) {
	tests := []struct {
		name        string
		coverage    string
		limitations []string
	}{
		{name: "full document", coverage: ExternalSourceCoverageFullDocument, limitations: []string{}},
		{name: "exact excerpt", coverage: ExternalSourceCoverageExactExcerpt, limitations: []string{"Only the selected issue section was requested."}},
		{name: "truncated document", coverage: ExternalSourceCoverageTruncatedDocument, limitations: []string{"The connector returned only the first three pages."}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := reviewableExternalIngestionTestBatch(t, test.coverage, test.limitations)
			manifest := mustReviewableIngestionManifest(t, batch)
			receipt := mustReviewableIngestionReceipt(t, batch, manifest)
			current := reviewableIngestionTestQuery(batch, 0)
			reviewPackage, err := BuildSourceClaimReviewPackage(receipt, manifest, current)
			if err != nil {
				t.Fatalf("BuildSourceClaimReviewPackage() error = %v", err)
			}
			if reviewPackage.ProposalBasis.SourceCoverage != test.coverage ||
				!slices.Equal(reviewPackage.ProposalBasis.SourceLimitations, test.limitations) {
				t.Fatalf("source completeness = %q / %#v, want %q / %#v", reviewPackage.ProposalBasis.SourceCoverage, reviewPackage.ProposalBasis.SourceLimitations, test.coverage, test.limitations)
			}
			encoded, err := json.Marshal(reviewPackage)
			if err != nil {
				t.Fatalf("json.Marshal(review package) error = %v", err)
			}
			if len(test.limitations) == 0 && !strings.Contains(string(encoded), `"source_limitations":[]`) {
				t.Fatalf("full-document source limitations are not an explicit empty list: %s", encoded)
			}
			basisID, err := proposalBasisID(reviewPackage.ProposalBasis)
			if err != nil {
				t.Fatalf("proposalBasisID(review package basis) error = %v", err)
			}
			if basisID != reviewPackage.ProposalBasis.ID {
				t.Fatalf("proposal basis ID = %q, want self-consistent %q", reviewPackage.ProposalBasis.ID, basisID)
			}
			packageID, err := reviewPackageID(reviewPackage)
			if err != nil {
				t.Fatalf("reviewPackageID(review package) error = %v", err)
			}
			if packageID != reviewPackage.ID {
				t.Fatalf("review package ID = %q, want self-consistent %q", reviewPackage.ID, packageID)
			}
			subject := ExactReviewSubject{
				SubmissionReceiptID:  receipt.ID,
				ProposalManifestID:   manifest.ID,
				ProposalOccurrenceID: current.ProposalOccurrenceID,
				ProposalBasisID:      reviewPackage.ProposalBasis.ID,
				ReviewPackageID:      reviewPackage.ID,
			}
			if err := ValidateExactSourceClaimReviewSubject(receipt, manifest, current, reviewPackage, subject); err != nil {
				t.Fatalf("ValidateExactSourceClaimReviewSubject() error = %v", err)
			}
			changed := cloneReviewPackage(reviewPackage)
			changed.ProposalBasis.SourceCoverage += "-changed"
			assertKind(t, ValidateExactSourceClaimReviewSubject(receipt, manifest, current, changed, subject), ErrorReviewContractConflict)
		})
	}

	batch := reviewableExternalIngestionTestBatch(t, ExternalSourceCoverageTruncatedDocument, []string{"The connector returned only the first three pages."})
	manifest := mustReviewableIngestionManifest(t, batch)
	receipt := mustReviewableIngestionReceipt(t, batch, manifest)
	current := reviewableIngestionTestQuery(batch, 0)
	for _, raw := range []string{"null", `[]`, `[" padded "]`, `{}`} {
		candidate := cloneReviewableIngestionQuery(current)
		candidate.OriginMetadata[externalSourceOriginLimitsKey] = raw
		_, err := BuildSourceClaimReviewPackage(receipt, manifest, candidate)
		assertKind(t, err, ErrorReviewContractConflict)
	}
}

func TestReviewableIngestionRejectsUnsupportedBatchMaterial(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ReviewableSourceClaimBatch)
	}{
		{name: "unsupported proposal kind", mutate: func(batch *ReviewableSourceClaimBatch) { batch.Occurrences[0].ProposalKind = "relation" }},
		{name: "unsupported fingerprint version", mutate: func(batch *ReviewableSourceClaimBatch) {
			batch.Occurrences[0].ProposalFingerprintVersion = ProposalFingerprintCodeFactV1
		}},
		{name: "already admitted", mutate: func(batch *ReviewableSourceClaimBatch) {
			batch.Occurrences[0].AdmissionOutcome = admissionOutcomeAdmitted
		}},
		{name: "canonical node state", mutate: func(batch *ReviewableSourceClaimBatch) { batch.Occurrences[0].CanonicalRef = "node:unexpected" }},
		{name: "code fact", mutate: func(batch *ReviewableSourceClaimBatch) { batch.Occurrences[0].CodeFact = &ResolvedCodeFact{} }},
		{name: "code relation", mutate: func(batch *ReviewableSourceClaimBatch) { batch.Occurrences[0].CodeRelation = &ResolvedCodeRelation{} }},
		{name: "failed attempt", mutate: func(batch *ReviewableSourceClaimBatch) { batch.ExtractionAttempt.Status = attemptStatusFailed }},
		{name: "repository-bound run", mutate: func(batch *ReviewableSourceClaimBatch) {
			batch.ExtractionRun.RepositorySnapshotID = "repo-snapshot:unexpected"
		}},
		{name: "missing source refs", mutate: func(batch *ReviewableSourceClaimBatch) { batch.Occurrences[0].SourceRefs = nil }},
		{name: "occurrence identity drift", mutate: func(batch *ReviewableSourceClaimBatch) { batch.Occurrences[0].ID += "x" }},
		{name: "fingerprint drift", mutate: func(batch *ReviewableSourceClaimBatch) { batch.Occurrences[0].ProposalFingerprint += "x" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := reviewableIngestionTestBatch(t, 1, "")
			test.mutate(&batch)
			_, err := BuildProposalBatchManifest(batch)
			assertKind(t, err, ErrorReviewContractConflict)
		})
	}
}

func TestReviewableIngestionManifestCompletenessAndDuplicateReferences(t *testing.T) {
	t.Run("completed empty batch is explicit and terminal", func(t *testing.T) {
		batch := reviewableIngestionTestBatch(t, 0, "")
		manifest := mustReviewableIngestionManifest(t, batch)
		if manifest.ProposalCount != 0 || manifest.Entries == nil || len(manifest.Entries) != 0 {
			t.Fatalf("empty manifest = %+v, want count 0 and explicit empty entries", manifest)
		}
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("json.Marshal(empty manifest) error = %v", err)
		}
		if !strings.Contains(string(encoded), `"entries":[]`) {
			t.Fatalf("empty manifest does not encode an explicit entry list: %s", encoded)
		}
		receipt := mustReviewableIngestionReceipt(t, batch, manifest)
		if receipt.ProposalCount != 0 {
			t.Fatalf("empty receipt proposal count = %d, want 0", receipt.ProposalCount)
		}
	})

	t.Run("occurrence removed after materialization", func(t *testing.T) {
		batch := reviewableIngestionTestBatch(t, 2, "")
		batch.Occurrences = append([]ProposalOccurrence(nil), batch.Occurrences[:1]...)
		_, err := BuildProposalBatchManifest(batch)
		assertKind(t, err, ErrorReviewContractConflict)
	})

	t.Run("occurrence added after materialization", func(t *testing.T) {
		batch := reviewableIngestionTestBatch(t, 2, "")
		complete := reviewableIngestionTestBatch(t, 3, "")
		batch.Occurrences = append(append([]ProposalOccurrence(nil), batch.Occurrences...), complete.Occurrences[2])
		_, err := BuildProposalBatchManifest(batch)
		assertKind(t, err, ErrorReviewContractConflict)
	})

	t.Run("duplicate exact source ref", func(t *testing.T) {
		batch := reviewableIngestionTestBatch(t, 1, "")
		occurrence := &batch.Occurrences[0]
		occurrence.SourceRefs = append(occurrence.SourceRefs, occurrence.SourceRefs[0])
		fingerprint, err := proposalFingerprint(occurrence.StatementText, occurrence.SourceRefs)
		if err != nil {
			t.Fatalf("proposalFingerprint(duplicate refs) error = %v", err)
		}
		occurrence.ProposalFingerprint = fingerprint
		_, err = BuildProposalBatchManifest(batch)
		assertKind(t, err, ErrorReviewContractConflict)
	})

	t.Run("self-consistent truncated manifest", func(t *testing.T) {
		batch := reviewableIngestionTestBatch(t, 2, "")
		manifest := mustReviewableIngestionManifest(t, batch)
		manifest.Entries = append([]ProposalBatchManifestEntry(nil), manifest.Entries[:1]...)
		manifest.ProposalCount = len(manifest.Entries)
		var err error
		manifest.ID, err = proposalBatchManifestID(manifest)
		if err != nil {
			t.Fatalf("proposalBatchManifestID(truncated) error = %v", err)
		}
		_, err = BuildSubmissionReceipt(batch, manifest)
		assertKind(t, err, ErrorReviewContractConflict)
	})
}

func TestReviewableIngestionValidatorRejectsCurrentAuthorityDrift(t *testing.T) {
	artifacts := newReviewableIngestionTestArtifacts(t, "session:authority")
	tests := []struct {
		name   string
		mutate func(*ProposalQueryResult)
	}{
		{name: "statement", mutate: func(current *ProposalQueryResult) { current.StatementText += " changed" }},
		{name: "fingerprint", mutate: func(current *ProposalQueryResult) { current.ProposalFingerprint += "x" }},
		{name: "occurrence", mutate: func(current *ProposalQueryResult) { current.ProposalOccurrenceID += "x" }},
		{name: "origin metadata", mutate: func(current *ProposalQueryResult) { current.OriginMetadata["changed"] = "true" }},
		{name: "source ref", mutate: func(current *ProposalQueryResult) { current.SourceRefs[0].StartByte++ }},
		{name: "session", mutate: func(current *ProposalQueryResult) { current.ProducerSessionRef = "session:other" }},
		{name: "admission outcome", mutate: func(current *ProposalQueryResult) { current.AdmissionOutcome = admissionOutcomeAdmitted }},
		{name: "attempt status", mutate: func(current *ProposalQueryResult) { current.ExtractionAttemptStatus = attemptStatusFailed }},
		{name: "proposal kind", mutate: func(current *ProposalQueryResult) { current.ProposalKind = "relation" }},
		{name: "source binding", mutate: func(current *ProposalQueryResult) {
			current.SourceBindingKind = ProposalSourceBindingRepositorySnapshot
		}},
		{name: "canonical authority", mutate: func(current *ProposalQueryResult) { current.CanonicalRef = "node:unexpected" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := cloneReviewableIngestionQuery(artifacts.current)
			test.mutate(&current)
			if err := ValidateExactSourceClaimReviewSubject(
				artifacts.receipt,
				artifacts.manifest,
				current,
				artifacts.reviewPackage,
				artifacts.subject,
			); err == nil {
				t.Fatal("authority drift was accepted")
			}
		})
	}

	current := cloneReviewableIngestionQuery(artifacts.current)
	current.OriginMetadata["changed"] = "true"
	rebuilt, err := BuildSourceClaimReviewPackage(artifacts.receipt, artifacts.manifest, current)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewPackage(drifted metadata) error = %v", err)
	}
	if rebuilt.ID == artifacts.reviewPackage.ID || rebuilt.ProposalBasis.ID == artifacts.reviewPackage.ProposalBasis.ID {
		t.Fatalf("drifted current authority retained package/basis identity: %+v", rebuilt)
	}
	assertKind(t, ValidateExactSourceClaimReviewSubject(
		artifacts.receipt,
		artifacts.manifest,
		current,
		rebuilt,
		artifacts.subject,
	), ErrorReviewContractConflict)
}

func TestReviewableIngestionReviewPackageJSONExcludesDecisionAndAuthorityFields(t *testing.T) {
	artifacts := newReviewableIngestionTestArtifacts(t, "session:json-boundary")
	artifacts.current.OriginMetadata["reviewer"] = "alice"
	artifacts.current.OriginMetadata["decision"] = "approve"
	artifacts.current.OriginMetadata["approved"] = "true"
	artifacts.current.OriginMetadata["producer_session_ref"] = "smuggled"
	artifacts.current.OriginMetadata["canonical_ref"] = "smuggled"
	var err error
	artifacts.reviewPackage, err = BuildSourceClaimReviewPackage(
		artifacts.receipt,
		artifacts.manifest,
		artifacts.current,
	)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewPackage(smuggled metadata) error = %v", err)
	}
	encoded, err := json.Marshal(artifacts.reviewPackage)
	if err != nil {
		t.Fatalf("json.Marshal(review package) error = %v", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(review package) error = %v", err)
	}
	keys := make(map[string]struct{})
	collectReviewableIngestionJSONKeys(decoded, keys)
	for key := range keys {
		normalized := strings.ToLower(key)
		for _, forbidden := range []string{"reviewer", "decision", "session", "approved", "approval", "canonical"} {
			if strings.Contains(normalized, forbidden) {
				t.Fatalf("review package JSON exposes forbidden %q field %q: %s", forbidden, key, encoded)
			}
		}
	}
	for _, forbidden := range []string{"admission_outcome", "canonical_ref", "canonical_edge_ref", "producer_session_ref"} {
		if _, ok := keys[forbidden]; ok {
			t.Fatalf("review package JSON exposes forbidden field %q: %s", forbidden, encoded)
		}
	}
	if _, ok := keys["origin_metadata_hash"]; !ok {
		t.Fatalf("review package JSON omitted the origin metadata commitment: %s", encoded)
	}

	receiptJSON, err := json.Marshal(artifacts.receipt)
	if err != nil {
		t.Fatalf("json.Marshal(receipt) error = %v", err)
	}
	if !strings.Contains(string(receiptJSON), `"producer_session_ref":"session:json-boundary"`) {
		t.Fatalf("receipt omitted its non-identity session audit metadata: %s", receiptJSON)
	}
}

func TestReviewableIngestionRejectsInvalidTextAndBounds(t *testing.T) {
	batchTests := []struct {
		name string
		kind ErrorKind
		edit func(*ReviewableSourceClaimBatch)
	}{
		{name: "invalid UTF-8 statement", kind: ErrorInvalidUTF8, edit: func(batch *ReviewableSourceClaimBatch) { batch.Occurrences[0].StatementText = string([]byte{0xff}) }},
		{name: "NUL statement", kind: ErrorInvalidInput, edit: func(batch *ReviewableSourceClaimBatch) { batch.Occurrences[0].StatementText = "statement\x00text" }},
		{name: "invalid UTF-8 rendered view", kind: ErrorInvalidUTF8, edit: func(batch *ReviewableSourceClaimBatch) { batch.ExtractionView.Rendered = []byte{0xff} }},
		{name: "NUL rendered view", kind: ErrorInvalidUTF8, edit: func(batch *ReviewableSourceClaimBatch) { batch.ExtractionView.Rendered = []byte("text\x00") }},
		{name: "NUL origin metadata key", kind: ErrorInvalidInput, edit: func(batch *ReviewableSourceClaimBatch) { batch.SourceSnapshot.OriginMetadata["bad\x00key"] = "value" }},
		{name: "NUL session", kind: ErrorInvalidInput, edit: func(batch *ReviewableSourceClaimBatch) { batch.ExtractionRun.ProducerSessionRef = "session\x00value" }},
		{name: "oversized session", kind: ErrorInvalidInput, edit: func(batch *ReviewableSourceClaimBatch) {
			batch.ExtractionRun.ProducerSessionRef = strings.Repeat("s", ProducerSessionRefMaxBytes+1)
		}},
		{name: "negative span start", kind: ErrorSpanOutOfBounds, edit: func(batch *ReviewableSourceClaimBatch) { batch.Spans[0].StartByte = -1 }},
		{name: "span past view", kind: ErrorSpanOutOfBounds, edit: func(batch *ReviewableSourceClaimBatch) {
			batch.Spans[0].EndByte = len(batch.ExtractionView.Rendered) + 1
		}},
	}
	for _, test := range batchTests {
		t.Run(test.name, func(t *testing.T) {
			batch := reviewableIngestionTestBatch(t, 1, "")
			test.edit(&batch)
			_, err := BuildProposalBatchManifest(batch)
			assertKind(t, err, test.kind)
		})
	}

	queryTests := []struct {
		name string
		kind ErrorKind
		edit func(*ProposalQueryResult)
	}{
		{name: "invalid UTF-8 quoted text", kind: ErrorInvalidUTF8, edit: func(current *ProposalQueryResult) { current.SourceRefs[0].QuotedText = string([]byte{0xff}) }},
		{name: "NUL quoted text", kind: ErrorInvalidInput, edit: func(current *ProposalQueryResult) { current.SourceRefs[0].QuotedText = "quote\x00text" }},
		{name: "negative source ref start", kind: ErrorSpanOutOfBounds, edit: func(current *ProposalQueryResult) { current.SourceRefs[0].StartByte = -1 }},
		{name: "source ref byte length mismatch", kind: ErrorSpanOutOfBounds, edit: func(current *ProposalQueryResult) { current.SourceRefs[0].EndByte++ }},
	}
	for _, test := range queryTests {
		t.Run("query "+test.name, func(t *testing.T) {
			artifacts := newReviewableIngestionTestArtifacts(t, "")
			current := cloneReviewableIngestionQuery(artifacts.current)
			test.edit(&current)
			_, err := BuildSourceClaimReviewPackage(artifacts.receipt, artifacts.manifest, current)
			assertKind(t, err, test.kind)
		})
	}
}

type reviewableIngestionTestArtifacts struct {
	batch         ReviewableSourceClaimBatch
	manifest      ProposalBatchManifest
	receipt       SubmissionReceipt
	current       ProposalQueryResult
	reviewPackage ReviewPackage
	subject       ExactReviewSubject
}

func newReviewableIngestionTestArtifacts(t *testing.T, session string) reviewableIngestionTestArtifacts {
	t.Helper()
	batch := reviewableIngestionTestBatch(t, 1, session)
	manifest := mustReviewableIngestionManifest(t, batch)
	receipt := mustReviewableIngestionReceipt(t, batch, manifest)
	current := reviewableIngestionTestQuery(batch, 0)
	reviewPackage, err := BuildSourceClaimReviewPackage(receipt, manifest, current)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewPackage() error = %v", err)
	}
	return reviewableIngestionTestArtifacts{
		batch:         batch,
		manifest:      manifest,
		receipt:       receipt,
		current:       current,
		reviewPackage: reviewPackage,
		subject: ExactReviewSubject{
			SubmissionReceiptID:  receipt.ID,
			ProposalManifestID:   manifest.ID,
			ProposalOccurrenceID: current.ProposalOccurrenceID,
			ProposalBasisID:      reviewPackage.ProposalBasis.ID,
			ReviewPackageID:      reviewPackage.ID,
		},
	}
}

func reviewableIngestionTestBatch(t *testing.T, count int, session string) ReviewableSourceClaimBatch {
	t.Helper()
	input := testManualInput("reviewable-ingestion-source")
	source, err := buildManualSourceContext(input)
	if err != nil {
		t.Fatalf("buildManualSourceContext() error = %v", err)
	}
	attempt, err := buildAttemptContextFromSourceWithDefinitionAndSession(
		source,
		input.RequestID,
		input.AttemptNumber,
		ExtractorDefinitionInput{},
		session,
	)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSourceWithDefinitionAndSession() error = %v", err)
	}
	proposals := make([]ExtractorProposalOutput, 0, count)
	for index := range count {
		proposals = append(proposals, ExtractorProposalOutput{
			ProposalLocalID: fmt.Sprintf("proposal-%03d", index),
			StatementText:   fmt.Sprintf("Reviewable statement %03d.", index),
			EvidenceRefs:    []string{"span:S1"},
		})
	}
	batch, err := materializeReviewableBatch(attempt, FrozenExtractorOutput{Proposals: proposals})
	if err != nil {
		t.Fatalf("materializeReviewableBatch() error = %v", err)
	}
	batch.ExtractionAttempt.Status = attemptStatusSucceeded
	batch.ExtractionAttempt.OutputHash = batch.FixtureOutputHash
	return batch
}

func reviewableExternalIngestionTestBatch(t *testing.T, coverage string, limitations []string) ReviewableSourceClaimBatch {
	t.Helper()
	input := validExternalSourceEnvelope("reviewable-ingestion-external-" + coverage)
	input.Coverage = coverage
	input.Limitations = append([]string(nil), limitations...)
	prepared, err := prepareExternalSource(input)
	if err != nil {
		t.Fatalf("prepareExternalSource() error = %v", err)
	}
	source, err := buildManualSourceContext(prepared.manual)
	if err != nil {
		t.Fatalf("buildManualSourceContext() error = %v", err)
	}
	attempt, err := buildAttemptContextFromSourceWithDefinitionAndSession(
		source,
		prepared.manual.RequestID,
		1,
		ExtractorDefinitionInput{},
		"session:external-review",
	)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSourceWithDefinitionAndSession() error = %v", err)
	}
	batch, err := materializeReviewableBatch(attempt, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
		ProposalLocalID: "external-proposal",
		StatementText:   "The external connector owns collection.",
		EvidenceRefs:    []string{"span:S1"},
	}}})
	if err != nil {
		t.Fatalf("materializeReviewableBatch() error = %v", err)
	}
	batch.ExtractionAttempt.Status = attemptStatusSucceeded
	batch.ExtractionAttempt.OutputHash = batch.FixtureOutputHash
	return batch
}

func reviewableIngestionTestQuery(batch ReviewableSourceClaimBatch, index int) ProposalQueryResult {
	occurrence := batch.Occurrences[index]
	return ProposalQueryResult{
		ProposalOccurrenceID:       occurrence.ID,
		ProposalLocalID:            occurrence.ProposalLocalID,
		ProposalFingerprint:        occurrence.ProposalFingerprint,
		ProposalFingerprintVersion: occurrence.ProposalFingerprintVersion,
		ProposalKind:               occurrence.ProposalKind,
		StatementText:              occurrence.StatementText,
		AdmissionOutcome:           occurrence.AdmissionOutcome,
		SourceRefs:                 append([]ResolvedSourceRef(nil), occurrence.SourceRefs...),
		ExtractionAttemptID:        batch.ExtractionAttempt.ID,
		ExtractionAttemptStatus:    batch.ExtractionAttempt.Status,
		ExtractionRunID:            batch.ExtractionRun.ID,
		ProducerSessionRef:         batch.ExtractionRun.ProducerSessionRef,
		ExtractorDefinitionID:      batch.ExtractorDefinition.ID,
		ExtractorName:              batch.ExtractorDefinition.Name,
		ExtractorVersion:           batch.ExtractorDefinition.Version,
		ExtractorConfigHash:        batch.ExtractorDefinition.ConfigHash,
		SourceBindingKind:          ProposalSourceBindingSourceSnapshot,
		ExtractionViewID:           batch.ExtractionView.ID,
		RendererName:               batch.ExtractionView.RendererName,
		RendererVersion:            batch.ExtractionView.RendererVersion,
		RenderedContentHash:        batch.ExtractionView.RenderedContentHash,
		SourceSnapshotID:           batch.SourceSnapshot.ID,
		SourceSystem:               batch.SourceSnapshot.SourceSystem,
		SourceID:                   batch.SourceSnapshot.SourceID,
		SourceVersion:              batch.SourceSnapshot.SourceVersion,
		RawContentHash:             batch.SourceSnapshot.RawContentHash,
		OriginMetadata:             cloneStringMap(batch.SourceSnapshot.OriginMetadata),
	}
}

func cloneReviewableIngestionQuery(input ProposalQueryResult) ProposalQueryResult {
	input.SourceRefs = append([]ResolvedSourceRef(nil), input.SourceRefs...)
	input.OriginMetadata = cloneStringMap(input.OriginMetadata)
	return input
}

func mustReviewableIngestionManifest(t *testing.T, batch ReviewableSourceClaimBatch) ProposalBatchManifest {
	t.Helper()
	manifest, err := BuildProposalBatchManifest(batch)
	if err != nil {
		t.Fatalf("BuildProposalBatchManifest() error = %v", err)
	}
	return manifest
}

func mustReviewableIngestionReceipt(t *testing.T, batch ReviewableSourceClaimBatch, manifest ProposalBatchManifest) SubmissionReceipt {
	t.Helper()
	receipt, err := BuildSubmissionReceipt(batch, manifest)
	if err != nil {
		t.Fatalf("BuildSubmissionReceipt() error = %v", err)
	}
	return receipt
}

func collectReviewableIngestionJSONKeys(value any, keys map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			keys[key] = struct{}{}
			collectReviewableIngestionJSONKeys(child, keys)
		}
	case []any:
		for _, child := range typed {
			collectReviewableIngestionJSONKeys(child, keys)
		}
	}
}

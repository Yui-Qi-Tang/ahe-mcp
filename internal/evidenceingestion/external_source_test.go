package evidenceingestion

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPrepareExternalSourceBuildsProviderNeutralAuthority(t *testing.T) {
	input := validExternalSourceEnvelope("external-1")
	input.ObservedAt = "2026-08-23T10:03:04+08:00"
	input.Coverage = ExternalSourceCoverageExactExcerpt
	input.Limitations = []string{"  comments after page 1 were not requested  "}
	originalLimitation := input.Limitations[0]

	prepared, err := prepareExternalSource(input)
	if err != nil {
		t.Fatalf("prepareExternalSource() error = %v", err)
	}
	if got, want := prepared.manual.SourceSystem, SourceSystemExternalDocument; got != want {
		t.Fatalf("source system = %q, want %q", got, want)
	}
	if got := prepared.manual.SourceID; !strings.HasPrefix(got, "extsrc:") {
		t.Fatalf("source ID = %q, want extsrc prefix", got)
	}
	if got, want := prepared.manual.SourceVersion, input.Revision; got != want {
		t.Fatalf("source version = %q, want %q", got, want)
	}
	if got, want := string(prepared.manual.Raw), input.Content; got != want {
		t.Fatalf("raw content = %q, want exact %q", got, want)
	}
	if got, want := prepared.envelope.ObservedAt, "2026-08-23T02:03:04Z"; got != want {
		t.Fatalf("observed_at = %q, want %q", got, want)
	}
	if got, want := prepared.envelope.Limitations[0], "comments after page 1 were not requested"; got != want {
		t.Fatalf("normalized limitation = %q, want %q", got, want)
	}
	if input.Limitations[0] != originalLimitation {
		t.Fatalf("prepareExternalSource mutated caller limitation = %q", input.Limitations[0])
	}
	if got := prepared.manual.OriginMetadata[externalSourceOriginFidelityKey]; got != ExternalSourceContentFidelityVerbatim {
		t.Fatalf("origin content fidelity = %q", got)
	}
	if got := prepared.manual.OriginMetadata[externalSourceOriginGlobalAbsenceKey]; got != "false" {
		t.Fatalf("global absence inference flag = %q, want false", got)
	}
}

func TestExternalSourceIdentityIsConnectorNeutralAndRevisionImmutable(t *testing.T) {
	first, err := prepareExternalSource(validExternalSourceEnvelope("external-1"))
	if err != nil {
		t.Fatalf("prepare first source: %v", err)
	}
	secondInput := validExternalSourceEnvelope("external-2")
	secondInput.CollectorID = "codex"
	secondInput.ConnectorID = "jira-api"
	secondInput.Content = "changed bytes for the same provider revision"
	second, err := prepareExternalSource(secondInput)
	if err != nil {
		t.Fatalf("prepare second source: %v", err)
	}
	if first.manual.SourceID != second.manual.SourceID {
		t.Fatalf("external source IDs differ by connector: %q != %q", first.manual.SourceID, second.manual.SourceID)
	}
	firstSnapshot, _, _, err := buildManualSource(first.manual)
	if err != nil {
		t.Fatalf("build first source: %v", err)
	}
	secondSnapshot, _, _, err := buildManualSource(second.manual)
	if err != nil {
		t.Fatalf("build second source: %v", err)
	}
	if firstSnapshot.ID != secondSnapshot.ID {
		t.Fatalf("same external revision snapshot IDs differ: %q != %q", firstSnapshot.ID, secondSnapshot.ID)
	}
	if firstSnapshot.RawContentHash == secondSnapshot.RawContentHash {
		t.Fatal("changed external source bytes kept the same content hash")
	}

	newRevision := validExternalSourceEnvelope("external-3")
	newRevision.Revision = "2026-08-24T02:00:00Z"
	third, err := prepareExternalSource(newRevision)
	if err != nil {
		t.Fatalf("prepare new revision: %v", err)
	}
	thirdSnapshot, _, _, err := buildManualSource(third.manual)
	if err != nil {
		t.Fatalf("build new revision: %v", err)
	}
	if firstSnapshot.ID == thirdSnapshot.ID {
		t.Fatalf("different external revisions share snapshot ID %q", firstSnapshot.ID)
	}
}

func TestCaptureExternalSourceReplaysAndRejectsRevisionMutation(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := validExternalSourceEnvelope("external-capture")

	first, err := captureExternalSource(ctx, db, input)
	if err != nil {
		t.Fatalf("first captureExternalSource() error = %v", err)
	}
	if first.Replayed {
		t.Fatal("first capture Replayed = true")
	}
	if first.ReceivedAt.IsZero() || first.ReceivedAt.Location() != time.UTC {
		t.Fatalf("received_at = %v, want AHE-controlled UTC time", first.ReceivedAt)
	}
	second, err := captureExternalSource(ctx, db, input)
	if err != nil {
		t.Fatalf("replay captureExternalSource() error = %v", err)
	}
	if !second.Replayed {
		t.Fatal("exact replay Replayed = false")
	}
	if second.SourceSnapshotID != first.SourceSnapshotID || !second.ReceivedAt.Equal(first.ReceivedAt) {
		t.Fatalf("replay result = %+v, want same snapshot and receipt as %+v", second, first)
	}
	newObservation := input
	newObservation.RequestID = "external-second-observation"
	newObservation.CollectorID = "codex"
	newObservation.ConnectorID = "jira-api"
	newObservation.ObservedAt = "2026-08-23T03:03:04Z"
	observedAgain, err := captureExternalSource(ctx, db, newObservation)
	if err != nil {
		t.Fatalf("second observation captureExternalSource() error = %v", err)
	}
	if observedAgain.Replayed || observedAgain.SourceSnapshotID != first.SourceSnapshotID {
		t.Fatalf("second observation = %+v, want new receipt over same snapshot", observedAgain)
	}
	if got, want := len(db.externalSourceReceipts), 2; got != want {
		t.Fatalf("external receipt count = %d, want %d", got, want)
	}

	receiptConflict := input
	receiptConflict.ConnectorID = "different-connector"
	_, err = captureExternalSource(ctx, db, receiptConflict)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	conflict := input
	conflict.RequestID = "external-conflict"
	conflict.Content = "same provider revision, different bytes"
	_, err = captureExternalSource(ctx, db, conflict)
	assertKind(t, err, ErrorOccurrenceConflict)
	if got, want := len(db.sourceSnapshots), 1; got != want {
		t.Fatalf("source snapshot count after conflict = %d, want %d", got, want)
	}
	if got := len(db.proposalOccurrences); got != 0 {
		t.Fatalf("proposal occurrence count = %d, want 0", got)
	}
}

func TestPrepareExternalSourceRejectsAmbiguousOrSummarizedInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ExternalSourceEnvelopeV1)
		kind   ErrorKind
	}{
		{
			name: "summary fidelity",
			mutate: func(input *ExternalSourceEnvelopeV1) {
				input.ContentFidelity = "summary"
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "excerpt without limitation",
			mutate: func(input *ExternalSourceEnvelopeV1) {
				input.Coverage = ExternalSourceCoverageExactExcerpt
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "full with limitation",
			mutate: func(input *ExternalSourceEnvelopeV1) {
				input.Limitations = []string{"attachments omitted"}
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "unversioned envelope",
			mutate: func(input *ExternalSourceEnvelopeV1) {
				input.SchemaVersion = ""
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "invalid provider token",
			mutate: func(input *ExternalSourceEnvelopeV1) {
				input.SourceSystem = "Jira Cloud"
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "missing observed time",
			mutate: func(input *ExternalSourceEnvelopeV1) {
				input.ObservedAt = ""
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "source time reversal",
			mutate: func(input *ExternalSourceEnvelopeV1) {
				input.SourceCreatedAt = "2026-08-24T00:00:00Z"
				input.SourceUpdatedAt = "2026-08-23T00:00:00Z"
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "too many lines",
			mutate: func(input *ExternalSourceEnvelopeV1) {
				input.Content = strings.Repeat("x\n", ExternalSourceContentMaxLines)
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "invalid utf8",
			mutate: func(input *ExternalSourceEnvelopeV1) {
				input.Content = string([]byte{0xff})
			},
			kind: ErrorInvalidUTF8,
		},
		{
			name: "invalid json content",
			mutate: func(input *ExternalSourceEnvelopeV1) {
				input.ContentFormat = ExternalSourceContentFormatJSON
				input.Content = `{"key":`
			},
			kind: ErrorInvalidInput,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validExternalSourceEnvelope("external-invalid")
			tc.mutate(&input)
			_, err := prepareExternalSource(input)
			assertKind(t, err, tc.kind)
		})
	}
}

func validExternalSourceEnvelope(requestID string) ExternalSourceEnvelopeV1 {
	return ExternalSourceEnvelopeV1{
		SchemaVersion:   ExternalSourceEnvelopeSchemaV1,
		RequestID:       requestID,
		SourceSystem:    "jira",
		SourceNamespace: "acme/eng",
		ObjectType:      "issue",
		ObjectID:        "AHE-42",
		Revision:        "2026-08-23T02:00:00Z",
		SourceLocation:  "https://acme.example/jira/AHE-42",
		Title:           "External intake boundary",
		ContentFormat:   ExternalSourceContentFormatMarkdown,
		ContentFidelity: ExternalSourceContentFidelityVerbatim,
		Content:         "# AHE-42\nExternal connector owns collection.\n",
		Coverage:        ExternalSourceCoverageFullDocument,
		CollectorID:     "claude-code",
		ConnectorID:     "atlassian-rovo",
		ObservedAt:      "2026-08-23T02:03:04Z",
		SourceCreatedAt: "2026-08-22T01:00:00Z",
		SourceUpdatedAt: "2026-08-23T02:00:00Z",
	}
}

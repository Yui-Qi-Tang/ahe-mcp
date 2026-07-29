package evidenceingestion

import (
	"context"
	"strings"
	"testing"
)

func TestLoadBoundedSourceViewMaterializesAvailableView(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("bounded-view-available"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}

	result, err := loadBoundedSourceView(
		ctx,
		db,
		source.SourceSnapshotID,
		source.ExtractionViewID,
	)
	if err != nil {
		t.Fatalf("loadBoundedSourceView() error = %v", err)
	}
	if result.Contract != BoundedSourceViewContractV1 ||
		result.Status != BoundedSourceViewStatusAvailable ||
		result.LimitReason != "" ||
		result.Input == nil {
		t.Fatalf("bounded result = %+v", result)
	}
	if result.Observation.RenderedBytes != int64(len(testManualInput("bounded-view-available").Raw)) ||
		result.Observation.SpanCount != 2 ||
		result.Input.SourceSnapshotID != source.SourceSnapshotID ||
		result.Input.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("bounded observation/input = %+v / %+v", result.Observation, result.Input)
	}
	if db.boundedViewPreflights != 1 || db.sourceViewLoads != 1 {
		t.Fatalf(
			"query counts = preflight %d/full %d, want 1/1",
			db.boundedViewPreflights,
			db.sourceViewLoads,
		)
	}
}

func TestLoadBoundedSourceViewWithholdsOverBudgetMaterial(t *testing.T) {
	tests := []struct {
		name       string
		raw        []byte
		wantReason BoundedSourceViewLimitReason
	}{
		{
			name:       "rendered bytes",
			raw:        []byte(strings.Repeat("x", int(BoundedSourceViewMaxRenderedBytesV1)+1)),
			wantReason: BoundedSourceViewLimitRenderedBytes,
		},
		{
			name:       "span count",
			raw:        []byte(strings.Repeat("x\n", int(BoundedSourceViewMaxSpansV1)+1)),
			wantReason: BoundedSourceViewLimitSpanCount,
		},
		{
			name:       "atomic span bytes",
			raw:        []byte(strings.Repeat("x", int(BoundedSourceViewMaxSpanBytesV1)+1) + "\n"),
			wantReason: BoundedSourceViewLimitAtomicSpan,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db := newMockSQLDB()
			input := boundedSourceViewTestInput(t.Name(), test.raw)
			source, err := captureManualSource(ctx, db, input)
			if err != nil {
				t.Fatalf("captureManualSource() error = %v", err)
			}

			result, err := loadBoundedSourceView(
				ctx,
				db,
				source.SourceSnapshotID,
				source.ExtractionViewID,
			)
			if err != nil {
				t.Fatalf("loadBoundedSourceView() error = %v", err)
			}
			if result.Status != BoundedSourceViewStatusOverBudget ||
				result.LimitReason != test.wantReason ||
				result.Input != nil {
				t.Fatalf("bounded result = %+v", result)
			}
			if db.boundedViewPreflights != 1 || db.sourceViewLoads != 0 {
				t.Fatalf(
					"query counts = preflight %d/full %d, want 1/0",
					db.boundedViewPreflights,
					db.sourceViewLoads,
				)
			}
		})
	}
}

func TestLoadBoundedSourceViewAcceptsAtomicSpanAtCap(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := boundedSourceViewTestInput(
		t.Name(),
		[]byte(strings.Repeat("x", int(BoundedSourceViewMaxSpanBytesV1))+"\n"),
	)
	source, err := captureManualSource(ctx, db, input)
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}

	result, err := loadBoundedSourceView(
		ctx,
		db,
		source.SourceSnapshotID,
		source.ExtractionViewID,
	)
	if err != nil {
		t.Fatalf("loadBoundedSourceView() error = %v", err)
	}
	if result.Status != BoundedSourceViewStatusAvailable ||
		result.Input == nil ||
		result.Observation.MaxSpanBytes != BoundedSourceViewMaxSpanBytesV1 {
		t.Fatalf("bounded result = %+v", result)
	}
	if db.boundedViewPreflights != 1 || db.sourceViewLoads != 1 {
		t.Fatalf(
			"query counts = preflight %d/full %d, want 1/1",
			db.boundedViewPreflights,
			db.sourceViewLoads,
		)
	}
}

func TestLoadBoundedSourceViewRejectsInvalidIdentityAndPreflightIntegrity(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("bounded-view-errors"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}

	_, err = loadBoundedSourceView(ctx, db, "source:bad", source.ExtractionViewID)
	assertKind(t, err, ErrorInvalidRecordID)
	_, err = loadBoundedSourceView(ctx, db, source.SourceSnapshotID, "extraction:bad")
	assertKind(t, err, ErrorInvalidRecordID)
	_, err = loadBoundedSourceView(ctx, db, "srcsnap:missing", "view:missing")
	assertKind(t, err, ErrorMissingSourceViewAttempt)

	view := db.extractionViews[source.ExtractionViewID]
	view.rendererVersion = "corrupt"
	db.extractionViews[source.ExtractionViewID] = view
	_, err = loadBoundedSourceView(
		ctx,
		db,
		source.SourceSnapshotID,
		source.ExtractionViewID,
	)
	assertKind(t, err, ErrorInvalidInput)
	if db.sourceViewLoads != 0 {
		t.Fatalf("corrupt preflight performed %d full source-view loads", db.sourceViewLoads)
	}
}

func boundedSourceViewTestInput(requestID string, raw []byte) ManualTextInput {
	return ManualTextInput{
		SourceSystem:  SourceSystemManualText,
		SourceID:      "bounded-source-" + requestID,
		SourceVersion: "v1",
		Raw:           raw,
		RequestID:     "bounded-source-request-" + requestID,
	}
}

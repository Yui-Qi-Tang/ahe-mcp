package evidenceingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"

	"github.com/jackc/pgx/v5"
)

const closureAdmissionEventDecisionEdgeIDsIndex = 15

func TestGetCanonicalSupersessionCurrentnessRequiresPool(t *testing.T) {
	t.Parallel()

	_, err := GetCanonicalSupersessionCurrentness(context.Background(), nil, "lineage:v1:sha256:"+strings.Repeat("0", 64))
	if kind, ok := KindOf(err); !ok || kind != ErrorInvalidInput {
		t.Fatalf("GetCanonicalSupersessionCurrentness() error = %v, want %s", err, ErrorInvalidInput)
	}
}

func TestGetCanonicalSupersessionCurrentnessRejectsInvalidLineageBeforeBegin(t *testing.T) {
	t.Parallel()

	db := &closureScriptDB{}
	_, err := getCanonicalSupersessionCurrentness(context.Background(), db, "lineage:caller-label")
	if kind, ok := KindOf(err); !ok || kind != ErrorInvalidRecordID {
		t.Fatalf("getCanonicalSupersessionCurrentness() error = %v, want %s", err, ErrorInvalidRecordID)
	}
	if db.began {
		t.Fatal("invalid lineage began a transaction")
	}
}

func TestGetCanonicalSupersessionCurrentnessMapsMissingLineage(t *testing.T) {
	t.Parallel()

	tx := &closureScriptTx{
		queryRowFunc: func(_ string, _ ...any) sqlRow {
			return closureScriptRow{err: pgx.ErrNoRows}
		},
	}
	db := &closureScriptDB{tx: tx}
	lineageKey := "lineage:v1:sha256:" + strings.Repeat("0", 64)
	_, err := getCanonicalSupersessionCurrentness(context.Background(), db, lineageKey)
	if kind, ok := KindOf(err); !ok || kind != ErrorSupersessionLineageNotFound {
		t.Fatalf("getCanonicalSupersessionCurrentness() error = %v, want supersession_lineage_not_found", err)
	}
	if tx.committed {
		t.Fatal("missing-lineage closure read committed")
	}
	if !tx.rolledBack {
		t.Fatal("missing-lineage closure read did not roll back")
	}
}

func TestGetCanonicalSupersessionCurrentnessUsesUnfilteredRepeatableRead(t *testing.T) {
	t.Parallel()

	injected := errors.New("injected lineage read failure")
	tx := &closureScriptTx{
		queryRowFunc: func(_ string, _ ...any) sqlRow {
			return closureScriptRow{err: injected}
		},
	}
	db := &closureScriptDB{tx: tx}
	_, err := getCanonicalSupersessionCurrentness(
		context.Background(),
		db,
		"lineage:v1:sha256:"+strings.Repeat("0", 64),
	)
	if !errors.Is(err, injected) {
		t.Fatalf("getCanonicalSupersessionCurrentness() error = %v, want injected failure", err)
	}
	wantExec := []string{
		"SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY",
		"SET LOCAL row_security = off",
	}
	if !reflect.DeepEqual(tx.execs, wantExec) {
		t.Fatalf("transaction execs = %#v, want %#v", tx.execs, wantExec)
	}
	if tx.committed {
		t.Fatal("failed closure read committed")
	}
	if !tx.rolledBack {
		t.Fatal("failed closure read did not roll back")
	}
}

func TestCanonicalSupersessionClosureInvariantErrorDoesNotNestDomainKind(t *testing.T) {
	t.Parallel()

	want := newDomainError(ErrorSupersessionInvariant, "persisted cut is inconsistent")
	got := canonicalSupersessionClosureInvariantError(want)
	if got != want {
		t.Fatalf("canonicalSupersessionClosureInvariantError() = %v, want original DomainError", got)
	}
	if strings.Count(got.Error(), string(ErrorSupersessionInvariant)) != 1 {
		t.Fatalf("canonicalSupersessionClosureInvariantError() = %q, want one stable kind", got)
	}
}

func TestLoadCanonicalSupersessionClosureEventsRejectsMaxPlusOne(t *testing.T) {
	t.Parallel()

	row := closureAdmissionEventRow(t)
	queryer := closureScriptQueryer{
		queryFunc: func(query string, args ...any) (sqlRows, error) {
			if !strings.Contains(query, "canonical supersession closure events") {
				return nil, fmt.Errorf("unexpected query: %s", compactSQL(query))
			}
			if !reflect.DeepEqual(args, []any{2}) {
				return nil, fmt.Errorf("event query args = %#v, want [2]", args)
			}
			return &closureScriptRows{rows: [][]any{row, row}}, nil
		},
	}
	_, err := loadCanonicalSupersessionClosureEvents(context.Background(), queryer, 1)
	if kind, ok := KindOf(err); !ok || kind != ErrorSupersessionInvariant ||
		!strings.Contains(err.Error(), "cannot truncate history") {
		t.Fatalf("loadCanonicalSupersessionClosureEvents() error = %v, want max+1 invariant", err)
	}
}

func TestLoadCanonicalSupersessionClosureEventsRejectsDecisionEdgeDrift(t *testing.T) {
	t.Parallel()

	base := closureAdmissionEventRow(t)
	var validEdgeIDs []string
	if err := json.Unmarshal(
		base[closureAdmissionEventDecisionEdgeIDsIndex].([]byte),
		&validEdgeIDs,
	); err != nil {
		t.Fatalf("unmarshal fixture decision edge IDs: %v", err)
	}
	duplicateEdgeIDs, err := json.Marshal(append(validEdgeIDs, validEdgeIDs[0]))
	if err != nil {
		t.Fatalf("marshal duplicate decision edge IDs: %v", err)
	}

	for _, test := range []struct {
		name   string
		raw    []byte
		detail string
	}{
		{name: "missing supersedes edge", raw: []byte(`[]`), detail: "omits supersedes edge"},
		{name: "missing decision row", raw: []byte(`null`), detail: "JSON string array"},
		{name: "not a string array", raw: []byte(`{"edge":"canon-edge:wrong-shape"}`), detail: "JSON string array"},
		{name: "duplicate edge", raw: duplicateEdgeIDs, detail: "duplicate edge"},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := append([]any(nil), base...)
			row[closureAdmissionEventDecisionEdgeIDsIndex] = test.raw
			queryer := closureScriptQueryer{
				queryFunc: func(string, ...any) (sqlRows, error) {
					return &closureScriptRows{rows: [][]any{row}}, nil
				},
			}
			_, err := loadCanonicalSupersessionClosureEvents(context.Background(), queryer, 2)
			if kind, ok := KindOf(err); !ok || kind != ErrorSupersessionInvariant ||
				!strings.Contains(err.Error(), test.detail) {
				t.Fatalf("loadCanonicalSupersessionClosureEvents() error = %v, want %s invariant", err, test.detail)
			}
		})
	}
}

func TestLoadCanonicalSupersessionTargetEdgesRejectsMissingGraphEdge(t *testing.T) {
	t.Parallel()

	queryer := closureScriptQueryer{
		queryFunc: func(string, ...any) (sqlRows, error) {
			return &closureScriptRows{rows: [][]any{{
				"admission-event:v2:sha256:" + strings.Repeat("1", 64),
				"lineage:v1:sha256:" + strings.Repeat("2", 64),
				"canon-node:new",
				"canon-node:old",
				"canon-edge:replacement",
				"", "", "", "", "", "", "",
			}}}, nil
		},
	}
	_, err := loadCanonicalSupersessionTargetEdges(
		context.Background(),
		queryer,
		"lineage:v1:sha256:"+strings.Repeat("2", 64),
		2,
	)
	if kind, ok := KindOf(err); !ok || kind != ErrorSupersessionInvariant ||
		!strings.Contains(err.Error(), "lacks its exact edge/target authority mirror") {
		t.Fatalf("loadCanonicalSupersessionTargetEdges() error = %v, want missing edge invariant", err)
	}
}

func TestLoadCanonicalSupersessionObjectClaimsKeepsUnclassifiedClaim(t *testing.T) {
	t.Parallel()

	basis := closureBasis()
	queryer := closureScriptQueryer{
		queryFunc: func(string, ...any) (sqlRows, error) {
			return &closureScriptRows{rows: [][]any{{
				"canon-node:unclassified",
				"",
				"", "", "", "", "", "", "",
				ExternalSourceEnvelopeSchemaV1,
				ExternalSourceContentFidelityVerbatim,
			}}}, nil
		},
	}
	claims, err := loadCanonicalSupersessionObjectClaims(context.Background(), queryer, basis, 2)
	if err != nil {
		t.Fatalf("loadCanonicalSupersessionObjectClaims() error = %v", err)
	}
	want := []evidencesupersession.ObjectClaimClassification{{NodeID: "canon-node:unclassified"}}
	if !reflect.DeepEqual(claims, want) {
		t.Fatalf("object claims = %#v, want %#v", claims, want)
	}
}

func closureAdmissionEventRow(t *testing.T) []any {
	t.Helper()

	basis := closureBasis()
	request, err := evidencesupersession.NewRequestPayloadV2(
		"occ:closure",
		"canon-node:new",
		basis,
		[]string{"canon-node:old"},
		0,
		"",
	)
	if err != nil {
		t.Fatalf("NewRequestPayloadV2() error = %v", err)
	}
	decision, err := evidencesupersession.NewDecisionPayloadV2(
		"occ:closure",
		"canon-node:new",
		"reviewer",
		"approved for closure loader test",
	)
	if err != nil {
		t.Fatalf("NewDecisionPayloadV2() error = %v", err)
	}
	event, err := evidencesupersession.NewAtomicReplacementEventV2(
		request,
		decision,
		"adm:closure",
		[]string{"canon-node:old"},
	)
	if err != nil {
		t.Fatalf("NewAtomicReplacementEventV2() error = %v", err)
	}
	eventID, err := event.ID()
	if err != nil {
		t.Fatalf("AdmissionEventPayloadV2.ID() error = %v", err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal(event) error = %v", err)
	}
	decisionEdgeIDs, err := json.Marshal([]string{
		"canon-edge:supports-claim",
		evidencegraph.StableCanonicalID(
			"canon-edge",
			event.ReplacementNodeID,
			event.TargetNodeIDs[0],
			string(evidencegraph.CanonicalSupersedes),
		),
	})
	if err != nil {
		t.Fatalf("json.Marshal(decision edge IDs) error = %v", err)
	}
	return []any{
		eventID,
		event.ContractVersion,
		event.Revision,
		event.PreviousRevision,
		event.PreviousEventID,
		event.Kind,
		event.LineageKey,
		event.ReplacementNodeID,
		event.ProposalOccurrence,
		event.AdmissionDecisionID,
		admissionOutcomeAdmitted,
		event.RequestPayloadHash,
		event.DecisionPayloadHash,
		decision.DecisionBy,
		decision.DecisionReason,
		decisionEdgeIDs,
		payload,
		"sha256:" + strings.TrimPrefix(eventID, "admission-event:v2:sha256:"),
	}
}

func closureBasis() evidencesupersession.Basis {
	return evidencesupersession.Basis{
		SourceSystem:    "jira",
		SourceNamespace: "tenant:closure",
		ObjectType:      "issue",
		ObjectID:        "AHE-41",
		SlotKind:        "field",
		SlotID:          "status",
	}
}

type closureScriptDB struct {
	tx       *closureScriptTx
	beginErr error
	began    bool
}

func (db *closureScriptDB) begin(context.Context) (sqlTx, error) {
	db.began = true
	if db.beginErr != nil {
		return nil, db.beginErr
	}
	if db.tx == nil {
		db.tx = &closureScriptTx{}
	}
	return db.tx, nil
}

func (db *closureScriptDB) query(context.Context, string, ...any) (sqlRows, error) {
	return nil, errors.New("unexpected query outside closure transaction")
}

func (db *closureScriptDB) queryRow(context.Context, string, ...any) sqlRow {
	return closureScriptRow{err: errors.New("unexpected query row outside closure transaction")}
}

type closureScriptTx struct {
	execs        []string
	queryFunc    func(string, ...any) (sqlRows, error)
	queryRowFunc func(string, ...any) sqlRow
	committed    bool
	rolledBack   bool
}

func (tx *closureScriptTx) exec(_ context.Context, query string, _ ...any) (execResult, error) {
	tx.execs = append(tx.execs, compactSQL(query))
	return closureScriptExecResult(0), nil
}

func (tx *closureScriptTx) query(_ context.Context, query string, args ...any) (sqlRows, error) {
	if tx.queryFunc == nil {
		return nil, fmt.Errorf("unexpected closure query: %s", compactSQL(query))
	}
	return tx.queryFunc(query, args...)
}

func (tx *closureScriptTx) queryRow(_ context.Context, query string, args ...any) sqlRow {
	if tx.queryRowFunc == nil {
		return closureScriptRow{err: fmt.Errorf("unexpected closure row query: %s", compactSQL(query))}
	}
	return tx.queryRowFunc(query, args...)
}

func (tx *closureScriptTx) commit(context.Context) error {
	tx.committed = true
	return nil
}

func (tx *closureScriptTx) rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

type closureScriptQueryer struct {
	queryFunc    func(string, ...any) (sqlRows, error)
	queryRowFunc func(string, ...any) sqlRow
}

func (queryer closureScriptQueryer) query(_ context.Context, query string, args ...any) (sqlRows, error) {
	if queryer.queryFunc == nil {
		return nil, fmt.Errorf("unexpected closure query: %s", compactSQL(query))
	}
	return queryer.queryFunc(query, args...)
}

func (queryer closureScriptQueryer) queryRow(_ context.Context, query string, args ...any) sqlRow {
	if queryer.queryRowFunc == nil {
		return closureScriptRow{err: fmt.Errorf("unexpected closure row query: %s", compactSQL(query))}
	}
	return queryer.queryRowFunc(query, args...)
}

type closureScriptExecResult int64

func (result closureScriptExecResult) RowsAffected() int64 {
	return int64(result)
}

type closureScriptRow struct {
	values []any
	err    error
}

func (row closureScriptRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	return scanClosureScriptValues(dest, row.values)
}

type closureScriptRows struct {
	rows  [][]any
	index int
	err   error
}

func (rows *closureScriptRows) Close() {}

func (rows *closureScriptRows) Err() error {
	return rows.err
}

func (rows *closureScriptRows) Next() bool {
	if rows.err != nil || rows.index >= len(rows.rows) {
		return false
	}
	rows.index++
	return true
}

func (rows *closureScriptRows) Scan(dest ...any) error {
	if rows.index < 1 || rows.index > len(rows.rows) {
		return errors.New("closure rows Scan called outside Next")
	}
	return scanClosureScriptValues(dest, rows.rows[rows.index-1])
}

func scanClosureScriptValues(dest []any, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("closure scan destination count = %d, want %d", len(dest), len(values))
	}
	for index, value := range values {
		if nullable, ok := dest[index].(*sql.NullString); ok {
			if value == nil {
				*nullable = sql.NullString{}
			} else {
				*nullable = sql.NullString{String: value.(string), Valid: true}
			}
			continue
		}
		target := reflect.ValueOf(dest[index])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return fmt.Errorf("closure scan destination %d is not a pointer", index)
		}
		source := reflect.ValueOf(value)
		if !source.IsValid() {
			target.Elem().SetZero()
			continue
		}
		if !source.Type().AssignableTo(target.Elem().Type()) {
			return fmt.Errorf(
				"closure scan value %d type %s cannot assign to %s",
				index,
				source.Type(),
				target.Elem().Type(),
			)
		}
		target.Elem().Set(source)
	}
	return nil
}

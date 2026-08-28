//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationCanonicalSupersessionCurrentnessIsDeterministicAndReadOnly(t *testing.T) {
	ctx, pool := integrationPool(t)
	lineageKey, event := prepareIntegrationClosurePair(t, ctx, pool, "validation-closure-deterministic")
	nodeIDs := validationClosureLineageNodeIDs(t, ctx, pool, lineageKey)

	beforeCounts := supersessionAuthorityCounts(t, ctx, pool)
	beforeHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head before deterministic reads: %v", err)
	}
	beforeTemporal := validationClosureTemporalSnapshot(t, ctx, pool, nodeIDs)

	first, err := GetCanonicalSupersessionCurrentness(ctx, pool, lineageKey)
	if err != nil {
		t.Fatalf("first GetCanonicalSupersessionCurrentness() error = %v", err)
	}
	second, err := GetCanonicalSupersessionCurrentness(ctx, pool, lineageKey)
	if err != nil {
		t.Fatalf("second GetCanonicalSupersessionCurrentness() error = %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("unchanged authority produced different currentness:\nfirst=%+v\nsecond=%+v", first, second)
	}
	assertIntegrationClosureBinding(t, first, event.EventRevision, event.AdmissionEventID, true)
	if first.Projection.Witness == nil || first.Projection.Witness.ID == "" ||
		first.Projection.HistoryHash == "" || first.Projection.CutHash == "" ||
		first.Projection.ObjectClaimManifestHash == "" {
		t.Fatalf("deterministic projection omitted an identity binding: %+v", first.Projection)
	}

	afterCounts := supersessionAuthorityCounts(t, ctx, pool)
	afterHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head after deterministic reads: %v", err)
	}
	afterTemporal := validationClosureTemporalSnapshot(t, ctx, pool, nodeIDs)
	if beforeCounts != afterCounts || beforeHead != afterHead || !reflect.DeepEqual(beforeTemporal, afterTemporal) {
		t.Fatalf(
			"currentness reads mutated authority: counts %+v -> %+v, head %+v -> %+v, temporal %v -> %v",
			beforeCounts,
			afterCounts,
			beforeHead,
			afterHead,
			beforeTemporal,
			afterTemporal,
		)
	}
}

func TestIntegrationCanonicalSupersessionCurrentnessRLSFailsClosed(t *testing.T) {
	ctx, pool := integrationPool(t)
	lineageKey, _ := prepareIntegrationClosurePair(t, ctx, pool, "validation-closure-rls")

	var schema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatalf("load validation schema: %v", err)
	}
	roleName := "validation_closure_rls_" + randomHex(t, 8)
	policyName := roleName + "_events"
	roleIdentifier := pgx.Identifier{roleName}.Sanitize()
	policyIdentifier := pgx.Identifier{policyName}.Sanitize()
	schemaIdentifier := pgx.Identifier{schema}.Sanitize()
	eventsTable := pgx.Identifier{schema, "canonical_supersession_admission_events"}.Sanitize()

	if _, err := pool.Exec(ctx, `CREATE ROLE `+roleIdentifier+` NOLOGIN`); err != nil {
		validationClosureSkipIfRolePermissionUnavailable(t, err, "create restricted RLS role")
		t.Fatalf("create restricted RLS role: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), validationClosureCleanupTimeout)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DROP POLICY IF EXISTS `+policyIdentifier+` ON `+eventsTable)
		_, _ = pool.Exec(cleanupCtx, `DROP OWNED BY `+roleIdentifier)
		_, _ = pool.Exec(cleanupCtx, `DROP ROLE IF EXISTS `+roleIdentifier)
	})

	var currentUser string
	if err := pool.QueryRow(ctx, `SELECT current_user`).Scan(&currentUser); err != nil {
		t.Fatalf("load current PostgreSQL role: %v", err)
	}
	currentUserIdentifier := pgx.Identifier{currentUser}.Sanitize()
	for _, setup := range []struct {
		operation string
		statement string
	}{
		{operation: "grant role membership", statement: `GRANT ` + roleIdentifier + ` TO ` + currentUserIdentifier},
		{operation: "grant schema usage", statement: `GRANT USAGE ON SCHEMA ` + schemaIdentifier + ` TO ` + roleIdentifier},
		{operation: "grant table reads", statement: `GRANT SELECT ON ALL TABLES IN SCHEMA ` + schemaIdentifier + ` TO ` + roleIdentifier},
		{operation: "enable event RLS", statement: `ALTER TABLE ` + eventsTable + ` ENABLE ROW LEVEL SECURITY`},
		{operation: "create event policy", statement: `CREATE POLICY ` + policyIdentifier + ` ON ` + eventsTable + ` FOR SELECT TO ` + roleIdentifier + ` USING (false)`},
	} {
		if _, err := pool.Exec(ctx, setup.statement); err != nil {
			validationClosureSkipIfRolePermissionUnavailable(t, err, setup.operation)
			t.Fatalf("%s: %v", setup.operation, err)
		}
	}

	restrictedConfig := pool.Config()
	previousAfterConnect := restrictedConfig.AfterConnect
	restrictedConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if previousAfterConnect != nil {
			if err := previousAfterConnect(ctx, conn); err != nil {
				return err
			}
		}
		_, err := conn.Exec(ctx, `SET ROLE `+roleIdentifier)
		return err
	}
	restrictedPool, err := pgxpool.NewWithConfig(ctx, restrictedConfig)
	if err != nil {
		validationClosureSkipIfRolePermissionUnavailable(t, err, "create restricted RLS pool")
		t.Fatalf("create restricted RLS pool: %v", err)
	}
	t.Cleanup(restrictedPool.Close)

	var visibleEvents int
	if err := restrictedPool.QueryRow(ctx, `SELECT count(*) FROM canonical_supersession_admission_events`).Scan(&visibleEvents); err != nil {
		t.Fatalf("confirm restricted event visibility: %v", err)
	}
	if visibleEvents != 0 {
		t.Fatalf("restricted role sees %d supersession events, want 0", visibleEvents)
	}

	beforeCounts := supersessionAuthorityCounts(t, ctx, pool)
	beforeHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("load head before restricted read: %v", err)
	}
	result, err := getCanonicalSupersessionCurrentness(ctx, pgxDB{pool: restrictedPool}, lineageKey)
	if err == nil {
		t.Fatalf("restricted currentness result = %+v, want fail-closed error", result)
	}
	if !reflect.DeepEqual(result, CanonicalSupersessionCurrentness{}) {
		t.Fatalf("restricted currentness returned a partial projection: %+v", result)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "row-level security") {
		t.Fatalf("restricted currentness error = %v, want row-level security rejection", err)
	}
	afterCounts := supersessionAuthorityCounts(t, ctx, pool)
	afterHead, headErr := GetCanonicalSupersessionHead(ctx, pool)
	if headErr != nil {
		t.Fatalf("load head after restricted read: %v", headErr)
	}
	if beforeCounts != afterCounts || beforeHead != afterHead {
		t.Fatalf("restricted read mutated authority: counts %+v -> %+v, head %+v -> %+v", beforeCounts, afterCounts, beforeHead, afterHead)
	}
}

func TestIntegrationCanonicalSupersessionCurrentnessRepeatableReadExcludesConcurrentCommit(t *testing.T) {
	ctx, pool := integrationPool(t)
	basis := supersessionIntegrationBasis()
	lineageKey, v2 := prepareIntegrationClosurePair(t, ctx, pool, "validation-closure-repeatable")
	before := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)

	v3Proposal := createSupersessionExternalProposal(
		t,
		ctx,
		pool,
		"validation-closure-repeatable-v3",
		"r3",
		"Policy value is v3.",
	)
	reached := make(chan struct{})
	release := make(chan struct{})
	barrierDB := &validationClosureBarrierDB{
		sqlDB:   pgxDB{pool: pool},
		reached: reached,
		release: release,
	}
	type validationClosureReadOutcome struct {
		result CanonicalSupersessionCurrentness
		err    error
	}
	readDone := make(chan validationClosureReadOutcome, 1)
	go func() {
		result, err := getCanonicalSupersessionCurrentness(ctx, barrierDB, lineageKey)
		readDone <- validationClosureReadOutcome{result: result, err: err}
	}()

	select {
	case <-reached:
	case outcome := <-readDone:
		t.Fatalf("currentness reader completed before the event-query barrier: result=%+v error=%v", outcome.result, outcome.err)
	case <-ctx.Done():
		t.Fatalf("waiting for currentness reader barrier: %v", ctx.Err())
	}

	v3, writerErr := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v3Proposal.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "v3 commits while the closure reader holds an older snapshot",
		Basis:                basis,
		TargetNodeIDs:        []string{v2.CanonicalRef},
		ExpectedRevision:     v2.EventRevision,
		ExpectedHeadEventID:  v2.AdmissionEventID,
	})
	close(release)
	outcome := <-readDone
	if writerErr != nil {
		t.Fatalf("commit v3 during closure read: %v", writerErr)
	}
	if outcome.err != nil {
		t.Fatalf("older repeatable-read closure error = %v", outcome.err)
	}
	if !reflect.DeepEqual(outcome.result, before) {
		t.Fatalf("reader mixed the concurrent commit into its old snapshot:\nbefore=%+v\nread=%+v", before, outcome.result)
	}

	after := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)
	assertIntegrationClosureBinding(t, after, v3.EventRevision, v3.AdmissionEventID, true)
	v2Currentness := validationClosureNodeCurrentness(t, after, v2.CanonicalRef)
	v3Currentness := validationClosureNodeCurrentness(t, after, v3.CanonicalRef)
	if v2Currentness.Status != evidencesupersession.CurrentnessSuperseded ||
		v3Currentness.Status != evidencesupersession.CurrentnessCurrent {
		t.Fatalf("fresh read currentness = v2:%+v v3:%+v", v2Currentness, v3Currentness)
	}
	if before.Projection.Head == after.Projection.Head ||
		before.Projection.HistoryHash == after.Projection.HistoryHash ||
		before.Projection.CutHash == after.Projection.CutHash ||
		before.Projection.ObjectClaimManifestHash == after.Projection.ObjectClaimManifestHash ||
		before.Projection.Witness == nil || after.Projection.Witness == nil ||
		before.Projection.Witness.ID == after.Projection.Witness.ID {
		t.Fatalf("fresh read did not bind the committed v3 cut: before=%+v after=%+v", before.Projection, after.Projection)
	}
}

const validationClosureCleanupTimeout = 10 * time.Second

type validationClosureBarrierDB struct {
	sqlDB
	reached chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (db *validationClosureBarrierDB) begin(ctx context.Context) (sqlTx, error) {
	tx, err := db.sqlDB.begin(ctx)
	if err != nil {
		return nil, err
	}
	return &validationClosureBarrierTx{
		sqlTx:   tx,
		reached: db.reached,
		release: db.release,
		once:    &db.once,
	}, nil
}

type validationClosureBarrierTx struct {
	sqlTx
	reached chan struct{}
	release <-chan struct{}
	once    *sync.Once
}

func (tx *validationClosureBarrierTx) query(ctx context.Context, query string, args ...any) (sqlRows, error) {
	if strings.Contains(query, "canonical supersession closure events") {
		tx.once.Do(func() { close(tx.reached) })
		select {
		case <-tx.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return tx.sqlTx.query(ctx, query, args...)
}

func validationClosureLineageNodeIDs(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	lineageKey string,
) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT canonical_node_id
		FROM canonical_supersession_members
		WHERE lineage_key = $1
		ORDER BY canonical_node_id
	`, lineageKey)
	if err != nil {
		t.Fatalf("load validation closure lineage members: %v", err)
	}
	defer rows.Close()
	nodeIDs := make([]string, 0)
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			t.Fatalf("scan validation closure lineage member: %v", err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate validation closure lineage members: %v", err)
	}
	if len(nodeIDs) == 0 {
		t.Fatal("validation closure lineage has no members")
	}
	return nodeIDs
}

func validationClosureTemporalSnapshot(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	nodeIDs []string,
) map[string]string {
	t.Helper()
	result := make(map[string]string, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		result[nodeID] = canonicalTemporalJSON(t, ctx, pool, nodeID)
	}
	return result
}

func validationClosureNodeCurrentness(
	t *testing.T,
	result CanonicalSupersessionCurrentness,
	nodeID string,
) evidencesupersession.NodeCurrentness {
	t.Helper()
	for _, node := range result.Projection.Nodes {
		if node.NodeID == nodeID {
			return node
		}
	}
	t.Fatalf("currentness projection omitted node %s: %+v", nodeID, result.Projection.Nodes)
	return evidencesupersession.NodeCurrentness{}
}

func validationClosureSkipIfRolePermissionUnavailable(t *testing.T, err error, operation string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" {
		t.Skipf("%s requires PostgreSQL role-management permission: %v", operation, err)
	}
}

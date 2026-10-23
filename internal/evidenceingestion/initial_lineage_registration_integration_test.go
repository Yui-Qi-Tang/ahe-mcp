//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: only synthetic claims in the explicitly selected disposable DB.
func TestIntegrationInitialRegistrationIssue24Baseline(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	first := createSupersessionExternalProposal(t, ctx, pool, "registration-original", "r1", "Policy value is v1.")
	admitted, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: first.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic initial admission"})
	if err != nil {
		t.Fatal(err)
	}
	second := createSupersessionExternalProposal(t, ctx, pool, "registration-reobserved", "r1", "Policy value is v1.")
	if first.ProposalOccurrenceID == second.ProposalOccurrenceID {
		t.Fatal("new delivery did not create a distinct pending occurrence")
	}
	var old, repeat ProposalQueryResult
	err = withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		var e error
		old, e = getProposalByOccurrenceID(ctx, tx, first.ProposalOccurrenceID)
		if e != nil {
			return e
		}
		repeat, e = getProposalByOccurrenceID(ctx, tx, second.ProposalOccurrenceID)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if old.SourceSnapshotID != repeat.SourceSnapshotID || old.ExtractionViewID != repeat.ExtractionViewID || old.RawContentHash != repeat.RawContentHash || !reflect.DeepEqual(old.SourceRefs, repeat.SourceRefs) {
		t.Fatal("unchanged observation changed source identity/grounding")
	}
	candidate, err := buildClaimNode(repeat)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ID != admitted.CanonicalRef {
		t.Fatal("new receipt/attempt changed C identity")
	}
	before := registrationBaselineCounts(t, ctx, pool)
	_, err = AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{ProposalOccurrenceID: repeat.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic self-target rejection", Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{admitted.CanonicalRef}})
	if err == nil || !strings.Contains(err.Error(), "cannot target itself") {
		t.Fatalf("self-target error=%v", err)
	}
	after := registrationBaselineCounts(t, ctx, pool)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed replacement changed counts: %v -> %v", before, after)
	}
	var outcome string
	if err := pool.QueryRow(ctx, `SELECT admission_outcome FROM proposal_occurrences WHERE proposal_occurrence_id=$1`, repeat.ProposalOccurrenceID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "pending" {
		t.Fatalf("self-target consumed pending occurrence: %s", outcome)
	}
	head, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if head.Revision != 0 || head.HeadEventID != "" {
		t.Fatal("failed replacement moved head")
	}
	if after[3] != 0 || after[4] != 0 {
		t.Fatal("observation/attempt alone granted membership")
	}
	t.Logf("B0/B1: distinct occurrence, same C=%s, same source/grounding, atomic self-target rejection, head=0, no membership", admitted.CanonicalRef)
}

func registrationBaselineCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []int {
	t.Helper()
	counts := make([]int, 6)
	for i, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "canonical_supersession_lineages", "canonical_supersession_members", "canonical_supersession_admission_events"} {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func TestIntegrationInitialRegistrationNativeCommitAndRetry(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	first := createSupersessionExternalProposal(t, ctx, pool, "registration-commit-original", "r1", "Policy value is v1.")
	admitted, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: first.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic initial admission"})
	if err != nil {
		t.Fatal(err)
	}
	createSupersessionExternalProposal(t, ctx, pool, "registration-commit-observed", "r1", "Policy value is v1.")
	var before string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(n)::text FROM canonical_graph_nodes n WHERE canonical_node_id=$1`, admitted.CanonicalRef).Scan(&before); err != nil {
		t.Fatal(err)
	}
	basis := supersessionIntegrationBasis()
	req := InitialLineageRegistrationRequest{ClaimID: admitted.CanonicalRef, ObservationReceiptID: "supersession-source-registration-commit-observed", Basis: evidencesupersession.Basis{SourceSystem: basis.SourceSystem, SourceNamespace: basis.SourceNamespace, ObjectType: basis.ObjectType, ObjectID: basis.ObjectID, SlotKind: basis.SlotKind, SlotID: basis.SlotID}}
	review, err := LoadInitialLineageRegistrationReview(ctx, pool, req)
	if err != nil {
		t.Fatal("native review", err)
	}
	if review.Binding.Anchor.Revision != 0 || review.Binding.Anchor.HeadEventID != nil {
		t.Fatal("review did not bind zero head")
	}
	in := InitialLineageRegistrationDecision{RequestID: "registration-1", Review: req, ExpectedSubject: review.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", Reason: "TEST APPROVAL STUB: first membership and derived effects reviewed"}
	bad := in
	bad.ExpectedSubject = "other-subject"
	if _, err := ApplyInitialLineageRegistration(ctx, pool, bad); err == nil {
		t.Fatal("unreviewed subject accepted")
	}
	result, err := ApplyInitialLineageRegistration(ctx, pool, in)
	if err != nil {
		t.Fatal("native commit", err)
	}
	again, err := ApplyInitialLineageRegistration(ctx, pool, in)
	if err != nil || !again.Replayed || again.Record.ID != result.Record.ID {
		t.Fatal("exact replay", err)
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(n)::text FROM canonical_graph_nodes n WHERE canonical_node_id=$1`, admitted.CanonicalRef).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("registration rewrote original C")
	}
	head, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil || head.Revision != 0 || head.HeadEventID != "" {
		t.Fatal("registration moved v2 head", err)
	}
	var initialID string
	var absent bool
	if err := pool.QueryRow(ctx, `SELECT initial_registration_event_id,first_admission_event_id IS NULL FROM canonical_supersession_members WHERE canonical_node_id=$1`, admitted.CanonicalRef).Scan(&initialID, &absent); err != nil {
		t.Fatal(err)
	}
	if initialID != result.Record.ID || !absent {
		t.Fatal("membership origin not independent registration")
	}
	projection, err := GetJointLineageCurrentness(ctx, pool, review.Binding.LineageKey)
	if err != nil || projection.Witness == nil || !projection.ClosureAvailable {
		t.Fatal("native J1 closure", err)
	}
	if len(projection.FrontierNodeIDs) != 1 || projection.FrontierNodeIDs[0] != admitted.CanonicalRef {
		t.Fatal("wrong initial frontier")
	}
	newer := createSupersessionExternalProposal(t, ctx, pool, "registration-real-v2", "r2", "Policy value is v2.")
	sr, err := LoadSupersessionReview(ctx, pool, SupersessionReviewRequest{ProposalOccurrenceID: newer.ProposalOccurrenceID, Basis: basis, TargetNodeIDs: []string{admitted.CanonicalRef}})
	if err != nil {
		t.Fatal("replacement review", err)
	}
	if len(sr.Display.TargetMemberships) != 1 || sr.Display.TargetMemberships[0].Origin.Kind != evidencesupersession.MembershipRegistration {
		t.Fatal("review omitted registration origin")
	}
	sin := ReviewedSupersessionInput{RequestID: "replacement-1", Review: sr.Display.Request, ExpectedSubject: sr.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "TEST APPROVAL STUB: genuine new revision"}
	replacement, err := AdmitReviewedSupersession(ctx, pool, sin)
	if err != nil {
		t.Fatal("native J2 replacement", err)
	}
	if replacement.Admission.EventRevision != 1 || len(replacement.Admission.BootstrappedTargetNodeIDs) != 0 {
		t.Fatal("replacement bootstrapped registered C")
	}
	projection, err = GetJointLineageCurrentness(ctx, pool, review.Binding.LineageKey)
	if err != nil || len(projection.FrontierNodeIDs) != 1 || projection.FrontierNodeIDs[0] != replacement.Admission.CanonicalRef {
		t.Fatal("native J2 closure", err)
	}
	replayReplacement, err := AdmitReviewedSupersession(ctx, pool, sin)
	if err != nil || !replayReplacement.Admission.Replayed {
		t.Fatal("v2 replay with registration authority", err)
	}
	again, err = ApplyInitialLineageRegistration(ctx, pool, in)
	if err != nil || !again.Replayed || again.Record.ID != result.Record.ID {
		t.Fatal("native J3 historical R retry", err)
	}
	changed := in
	changed.RequestID = "registration-2"
	if _, err := ApplyInitialLineageRegistration(ctx, pool, changed); err == nil {
		t.Fatal("second request duplicated membership")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM canonical_initial_lineage_registrations`).Scan(&count); err != nil || count != 1 {
		t.Fatal("registration was not single-effect", count, err)
	}
}

func registrationNativeFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label, objectID string) InitialLineageRegistrationDecision {
	t.Helper()
	first := createIntegrationClosureExternalProposal(t, ctx, pool, label+"-original", objectID, "r1", "Synthetic initial registration claim.")
	admitted, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: first.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic initial claim"})
	if err != nil {
		t.Fatal(err)
	}
	createIntegrationClosureExternalProposal(t, ctx, pool, label+"-observed", objectID, "r1", "Synthetic initial registration claim.")
	base := supersessionIntegrationBasis()
	req := InitialLineageRegistrationRequest{ClaimID: admitted.CanonicalRef, ObservationReceiptID: "supersession-closure-" + label + "-observed", Basis: evidencesupersession.Basis{SourceSystem: base.SourceSystem, SourceNamespace: base.SourceNamespace, ObjectType: base.ObjectType, ObjectID: objectID, SlotKind: base.SlotKind, SlotID: base.SlotID}}
	review, err := LoadInitialLineageRegistrationReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	return InitialLineageRegistrationDecision{RequestID: label + "-decision", Review: req, ExpectedSubject: review.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", Reason: "TEST APPROVAL STUB: independent first lineage registration"}
}

func TestIntegrationInitialRegistrationSameHeadCompetition(t *testing.T) {
	for _, sameRequest := range []bool{false, true} {
		t.Run(fmt.Sprint("same_request=", sameRequest), func(t *testing.T) {
			ctx, pool := integrationPoolWithMigrations(t)
			in := registrationNativeFixture(t, ctx, pool, "competition", "AHE-42")
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(ctx, `SELECT evidence_governance_lock_v1('evidence_governance_head'::regclass)`); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				receipt InitialLineageRegistrationReceipt
				err     error
			}
			results := make(chan outcome, 2)
			for i := range 2 {
				input := in
				if !sameRequest {
					input.RequestID = fmt.Sprintf("competition-%d", i)
				}
				go func() { r, e := ApplyInitialLineageRegistration(ctx, pool, input); results <- outcome{r, e} }()
			}
			waitInitialRegistrationLocks(t, ctx, pool, 2)
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			success, replays := 0, 0
			for range 2 {
				select {
				case r := <-results:
					if r.err == nil {
						success++
						if r.receipt.Replayed {
							replays++
						}
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if sameRequest {
				if success != 2 || replays != 1 {
					t.Fatalf("exact concurrent retries successes=%d replays=%d", success, replays)
				}
			} else if success != 1 {
				t.Fatalf("different request successes=%d", success)
			}
			var registrations, members, head int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM canonical_initial_lineage_registrations),(SELECT count(*) FROM canonical_supersession_members),(SELECT revision FROM canonical_supersession_admission_head)`).Scan(&registrations, &members, &head); err != nil {
				t.Fatal(err)
			}
			if registrations != 1 || members != 1 || head != 0 {
				t.Fatalf("same-head race partial effects R=%d members=%d head=%d", registrations, members, head)
			}
		})
	}
}

func waitInitialRegistrationLocks(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%SELECT evidence_governance_lock_v1%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= want {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("database lock barrier not observed: %d/%d", waiting, want)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestIntegrationInitialRegistrationHistoryAndCurrentManifest(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	in := registrationNativeFixture(t, ctx, pool, "history", "AHE-42")
	receipt, err := ApplyInitialLineageRegistration(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	key := receipt.Record.Payload.Binding.LineageKey
	complete, err := GetJointLineageCurrentness(ctx, pool, key)
	if err != nil || !complete.ClosureAvailable {
		t.Fatal(err)
	}
	extra := createIntegrationClosureExternalProposal(t, ctx, pool, "history-extra", "AHE-42", "r2", "Another unclassified claim.")
	if _, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: extra.ProposalOccurrenceID}); err != nil {
		t.Fatal(err)
	}
	incomplete, err := GetJointLineageCurrentness(ctx, pool, key)
	if err != nil || incomplete.Witness != nil || incomplete.ClosureAvailable || complete.CutHash == incomplete.CutHash {
		t.Fatal("current manifest was not refreshed", err)
	}
	withdrawal := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: in.Review.ClaimID, Operation: "withdraw"}, "history-withdrawal")
	if _, err := ApplyGovernanceDecision(ctx, pool, withdrawal); err != nil {
		t.Fatal(err)
	}
	retry, err := ApplyInitialLineageRegistration(ctx, pool, in)
	if err != nil || !retry.Replayed || retry.Record.ID != receipt.Record.ID {
		t.Fatal("current changes invalidated historical R", err)
	}
	down, err := os.ReadFile("../../migrations/000060_initial_lineage_registration.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("rollback erased independent authority")
	}
}

func initialRegistrationBoundedPool(t *testing.T, ctx context.Context, owner *pgxpool.Pool, profile dbrole.Profile) *pgxpool.Pool {
	t.Helper()
	suffix := randomHex(t, 6)
	group := "reg_group_" + suffix
	login := "reg_login_" + suffix
	var schema, database string
	if err := owner.QueryRow(ctx, `SELECT current_schema(),current_database()`).Scan(&schema, &database); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`REVOKE ALL ON SCHEMA public FROM PUBLIC`,
		`REVOKE UPDATE ON pg_catalog.pg_settings FROM PUBLIC`,
		`REVOKE CREATE,TEMPORARY ON DATABASE ` + pgx.Identifier{database}.Sanitize() + ` FROM PUBLIC`,
		`CREATE ROLE ` + pgx.Identifier{group}.Sanitize() + ` NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`,
		`CREATE ROLE ` + pgx.Identifier{login}.Sanitize() + ` LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`,
		`GRANT ` + pgx.Identifier{group}.Sanitize() + ` TO ` + pgx.Identifier{login}.Sanitize() + ` WITH INHERIT FALSE, SET TRUE`,
	} {
		if _, err := owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	connection, err := owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dbrole.InstallPolicy(ctx, connection.Conn(), dbrole.InstallInput{Role: group, Schema: schema, Profile: profile})
	connection.Release()
	if err != nil {
		t.Fatal("install bounded role", err)
	}
	config := owner.Config().Copy()
	config.ConnConfig.User = login
	config.ConnConfig.Password = ""
	config.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `SET ROLE `+pgx.Identifier{group}.Sanitize()); err != nil {
			return err
		}
		_, err := dbrole.VerifyRuntimeConnection(ctx, c, dbrole.RuntimeVerificationInput{Role: group, SessionUser: login, Schema: schema, Profile: profile})
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		owner.Exec(cleanup, `DROP OWNED BY `+pgx.Identifier{group}.Sanitize())
		owner.Exec(cleanup, `DROP ROLE `+pgx.Identifier{login}.Sanitize())
		owner.Exec(cleanup, `DROP ROLE `+pgx.Identifier{group}.Sanitize())
	})
	if err := pool.Ping(ctx); err != nil {
		t.Fatal("bounded LOGIN verification", err)
	}
	return pool
}

func TestIntegrationInitialRegistrationBoundedRolesAndRLS(t *testing.T) {
	ctx, owner := initialRegistrationRoleDatabase(t)
	if _, err := migrations.ApplyUp(ctx, owner); err != nil {
		t.Fatal(err)
	}
	in := registrationNativeFixture(t, ctx, owner, "roles", "AHE-42")
	reviewer := initialRegistrationBoundedPool(t, ctx, owner, dbrole.ProfileInitialLineageReviewer)
	query := initialRegistrationBoundedPool(t, ctx, owner, dbrole.ProfileQuery)
	if _, err := ApplyInitialLineageRegistration(ctx, query, in); err == nil {
		t.Fatal("Query acquired write authority")
	}
	for _, q := range []string{`INSERT INTO canonical_supersession_members SELECT * FROM canonical_supersession_members WHERE false`, `INSERT INTO canonical_graph_nodes SELECT * FROM canonical_graph_nodes WHERE false`, `INSERT INTO canonical_withdrawal_events SELECT * FROM canonical_withdrawal_events WHERE false`} {
		if _, err := reviewer.Exec(ctx, q); err == nil {
			t.Fatal("reviewer acquired unrelated write", q)
		}
	}
	receipt, err := ApplyInitialLineageRegistration(ctx, reviewer, in)
	if err != nil {
		t.Fatal("bounded registration", err)
	}
	key := receipt.Record.Payload.Binding.LineageKey
	scope := ConsistencyScope{Namespace: "rls-registration", ScopeRef: "release", Policy: ConsistencyFrontierPolicy}
	propositionTestBind(t, ctx, owner, in.Review.ClaimID, "rls-binding", PropositionKey{Namespace: scope.Namespace, ScopeRef: scope.ScopeRef, LocalID: "C", Revision: "1"})
	if p, err := GetJointLineageCurrentness(ctx, query, key); err != nil || p.Witness == nil {
		t.Fatal("bounded Query", err)
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE canonical_initial_lineage_registrations ENABLE ROW LEVEL SECURITY; CREATE POLICY hide_registrations ON canonical_initial_lineage_registrations USING(false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := GetJointLineageCurrentness(ctx, query, key); err == nil {
		t.Fatal("RLS-hidden registration interpreted as empty catalog")
	}
	if _, err := GetCanonicalSupersessionCurrentness(ctx, query, key); err == nil {
		t.Fatal("RLS-hidden registration selected legacy Query path")
	}
	if _, err := ReadEvidenceUseProjectionForPolicy(ctx, query, evidenceuse.CurrentActionV1, []string{in.Review.ClaimID}); err == nil {
		t.Fatal("RLS-hidden registration selected legacy assessment path")
	}
	if _, err := ReadConsistencyScope(ctx, query, scope); err == nil {
		t.Fatal("RLS-hidden registration selected legacy consistency path")
	}
}

func TestIntegrationInitialRegistrationRejectsNoncanonicalAuthority(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	in := registrationNativeFixture(t, ctx, pool, "noncanonical", "AHE-42")
	review, err := LoadInitialLineageRegistrationReview(ctx, pool, in.Review)
	if err != nil {
		t.Fatal(err)
	}
	payload := evidencesupersession.InitialRegistrationPayload{ContractVersion: evidencesupersession.InitialRegistrationContractV1, Binding: review.Binding, ReviewSubjectID: review.Subject, RequestID: in.RequestID, Reviewer: in.ReviewerID, Reason: in.Reason}
	body, err := json.MarshalIndent(payload, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	id := governanceDigest("initial-lineage-registration:v1:sha256:", body)
	binding, err := json.Marshal(struct {
		Contract string                                         `json:"contract_version"`
		Binding  evidencesupersession.RegistrationReviewBinding `json:"binding"`
	}{payload.ContractVersion, payload.Binding})
	if err != nil {
		t.Fatal(err)
	}
	grounding, err := json.Marshal(review.Grounding)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO canonical_initial_lineage_registrations(event_id,request_id,claim_id,lineage_key,body,binding_body,grounding_body) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, in.RequestID, in.Review.ClaimID, review.Binding.LineageKey, string(body), string(binding), string(grounding))
	if err == nil {
		t.Fatal("SQL accepted a noncanonical event whose ID cannot replay through the frozen domain contract")
	}
	if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err != nil {
		t.Fatal("rejected raw envelope changed native commit state", err)
	}
}

func TestIntegrationInitialRegistrationFailureInjection(t *testing.T) {
	for _, table := range []string{"canonical_supersession_lineages", "canonical_supersession_members", "canonical_initial_lineage_registrations"} {
		t.Run(table, func(t *testing.T) {
			ctx, pool := integrationPoolWithMigrations(t)
			in := registrationNativeFixture(t, ctx, pool, "rollback", "AHE-42")
			before := registrationBaselineCounts(t, ctx, pool)
			var revision int64
			if err := pool.QueryRow(ctx, `SELECT revision FROM evidence_governance_head`).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			q := `CREATE FUNCTION registration_test_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected registration failure'; END $$; CREATE TRIGGER zz_registration_test_failure AFTER INSERT ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION registration_test_failure()`
			if _, err := pool.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err == nil {
				t.Fatal("injected failure committed")
			}
			if after := registrationBaselineCounts(t, ctx, pool); !reflect.DeepEqual(before, after) {
				t.Fatal("partial registration mutation", before, after)
			}
			var current int64
			var count int
			if err := pool.QueryRow(ctx, `SELECT revision,(SELECT count(*) FROM canonical_initial_lineage_registrations) FROM evidence_governance_head`).Scan(&current, &count); err != nil {
				t.Fatal(err)
			}
			if revision != current || count != 0 {
				t.Fatal("failed transaction changed cut or receipt")
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER zz_registration_test_failure ON `+table); err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err != nil {
				t.Fatal("failure invalidated original exact review", err)
			}
		})
	}
}

func TestIntegrationInitialRegistrationInvalidatesPendingSupersessionReview(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	in := registrationNativeFixture(t, ctx, pool, "pending", "AHE-42")
	next := createSupersessionExternalProposal(t, ctx, pool, "pending-new-version", "r2", "Genuine new revision.")
	sr, err := LoadSupersessionReview(ctx, pool, SupersessionReviewRequest{ProposalOccurrenceID: next.ProposalOccurrenceID, Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{in.Review.ClaimID}})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := LoadInitialLineageRegistrationReview(ctx, pool, in.Review)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedSubject = fresh.Subject
	if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	old := ReviewedSupersessionInput{RequestID: "stale-replacement", Review: sr.Display.Request, ExpectedSubject: sr.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic replacement"}
	if _, err := AdmitReviewedSupersession(ctx, pool, old); err == nil {
		t.Fatal("same head reused stale target membership review")
	}
	head, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil || head.Revision != 0 {
		t.Fatal("stale review changed head", err)
	}
}

func TestIntegrationInitialRegistrationCanonicalUnicode(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	in := registrationNativeFixture(t, ctx, pool, "unicode", "AHE-42")
	in.Review.Basis.SlotID = "status<&>\"欄"
	review, err := LoadInitialLineageRegistrationReview(ctx, pool, in.Review)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedSubject = review.Subject
	in.ReviewerID = "reviewer<&>"
	in.Reason = "TEST APPROVAL STUB: quoted \"value\", <>&, x\u2028y\u2029z and\nnewline."
	receipt, err := ApplyInitialLineageRegistration(ctx, pool, in)
	if err != nil {
		t.Fatal("Go/SQL canonical unicode differs", err)
	}
	if receipt.Record.Payload.Reason != in.Reason {
		t.Fatal("canonical encoding changed review text")
	}
}

// Closed-profile verification inspects the whole database, so the fixture owns
// a fresh database rather than revoking PUBLIC privileges on DATABASE_DSN.
func initialRegistrationRoleDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	name := "ahe_initial_registration_" + randomHex(t, 6)
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		cancel()
		t.Fatal(err)
	}
	var owner *pgxpool.Pool
	t.Cleanup(func() {
		if owner != nil {
			owner.Close()
		}
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		admin.Exec(cleanup, `DROP DATABASE `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`)
		admin.Close(cleanup)
		cancel()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	config.ConnConfig.RuntimeParams["search_path"] = "evidence,pg_catalog"
	owner, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `CREATE SCHEMA evidence`); err != nil {
		t.Fatal(err)
	}
	return ctx, owner
}

func TestIntegrationInitialRegistrationDoesNotOrderByReceiptTime(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	in := registrationNativeFixture(t, ctx, pool, "clock", "AHE-42")
	// A synthetic timestamp fixture models equal/backward DB wall-clock readings.
	// All receipt bytes/hash/source links come from native capture; no OS clock is changed.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `ALTER TABLE external_source_intake_receipts DISABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE external_source_intake_receipts SET created_at=(SELECT created_at FROM admission_decisions WHERE canonical_ref=$1 AND outcome='admitted') WHERE request_id=$2`, in.Review.ClaimID, in.Review.ObservationReceiptID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE external_source_intake_receipts ENABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	review, err := LoadInitialLineageRegistrationReview(ctx, pool, in.Review)
	if err != nil {
		t.Fatal("receipt wall clock became an authority ordering rule", err)
	}
	in.ExpectedSubject = review.Subject
	if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err != nil {
		t.Fatal("time metadata rejected reviewed observation", err)
	}
}

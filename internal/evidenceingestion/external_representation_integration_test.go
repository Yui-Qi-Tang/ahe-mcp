//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func externalRepresentationCheck(t *testing.T, ctx context.Context, pool *pgxpool.Pool, r ExternalRepresentationRecord, request string) ExternalCheckRecord {
	t.Helper()
	material, err := r.CheckMaterial("representation-input")
	if err != nil {
		t.Fatal(err)
	}
	in := externalCheckFixture(r.Definition.Subject, request)
	in.Materials = []ExternalCheckMaterial{material}
	in.Findings[0].Criterion = "supplied structure is syntactically comparable; no semantic verdict"
	in.Findings[0].Result = "inconclusive"
	in.Findings[0].InputIDs = []string{material.ID}
	check, err := RecordExternalCheck(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	return check.Record
}

func TestIntegrationExternalRepresentationMissingIdentityAndSupplementHistory(t *testing.T) {
	ctx, pool := integrationPool(t)
	subject := externalCheckTestSubject(t, ctx, pool, "release", "1")
	supplied := externalCheckTestSubject(t, ctx, pool, "later-attachment", "1")
	before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
	first := externalRepresentationFixture(subject, "missing-a", "附件甲")
	second := externalRepresentationFixture(subject, "missing-b", "附件乙")
	a, err := RecordExternalRepresentation(ctx, pool, first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RecordExternalRepresentation(ctx, pool, second)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadExternalRepresentation(ctx, pool, a.Record.ID)
	if err != nil || !reflect.DeepEqual(read, a.Record) || read.Definition.Content != first.Content {
		t.Fatal("exact representation roundtrip failed", err)
	}
	check := externalRepresentationCheck(t, ctx, pool, a.Record, "before-supplement")
	link := ExternalRepresentationLink{check.ID, "representation-input", a.Record.ID}
	replayed, err := LinkExternalCheckRepresentation(ctx, pool, link)
	if err != nil || replayed {
		t.Fatal("first link", err)
	}
	replayed, err = LinkExternalCheckRepresentation(ctx, pool, link)
	if err != nil || !replayed {
		t.Fatal("link replay", err)
	}
	users, err := ReadExternalDependencyUsers(ctx, pool, first.Dependencies[0].Key, 10)
	if err != nil || len(users.Representations) != 1 || users.Representations[0].ID != a.Record.ID || len(users.CheckInputs) != 1 || users.CheckInputs[0] != link || users.RepresentationsTruncated || users.CheckInputsTruncated {
		t.Fatalf("attachment a users=%+v err=%v", users, err)
	}
	users, err = ReadExternalDependencyUsers(ctx, pool, second.Dependencies[0].Key, 10)
	if err != nil || len(users.Representations) != 1 || users.Representations[0].ID != b.Record.ID || len(users.CheckInputs) != 0 {
		t.Fatal("attachment b mixed with a", err)
	}
	sameNameDifferentScope := first.Dependencies[0].Key
	sameNameDifferentScope.ScopeRef = "another-contract"
	users, err = ReadExternalDependencyUsers(ctx, pool, sameNameDifferentScope, 10)
	if err != nil || len(users.Representations) != 0 || len(users.CheckInputs) != 0 {
		t.Fatal("external scope ignored", err)
	}

	next := externalRepresentationFixture(subject, "supplied-a", "附件甲")
	next.Version = "2"
	next.PreviousID = a.Record.ID
	next.RevisionReason = "補交附件甲；重新記錄完整表示"
	next.Dependencies[0].Status = "supplied"
	next.Dependencies[0].SourceSnapshotID = supplied.SourceSnapshotID
	next.Dependencies[0].ExtractionViewID = supplied.ExtractionViewID
	next.Dependencies[0].RenderedContentHash = supplied.RenderedContentHash
	newer, err := RecordExternalRepresentation(ctx, pool, next)
	if err != nil {
		t.Fatal(err)
	}
	if newer.Record.ID == a.Record.ID || next.Content != first.Content {
		t.Fatal("fixture or revision identity incorrect")
	}
	oldMaterial, _ := a.Record.CheckMaterial("input")
	newMaterial, _ := newer.Record.CheckMaterial("input")
	if oldMaterial.Content == newMaterial.Content {
		t.Fatal("supplement missing from actual checker input")
	}
	if _, err := LinkExternalCheckRepresentation(ctx, pool, ExternalRepresentationLink{check.ID, "representation-input", newer.Record.ID}); err == nil {
		t.Fatal("old check attached to new supplement")
	}
	freshCheck := externalRepresentationCheck(t, ctx, pool, newer.Record, "after-supplement")
	if _, err := LinkExternalCheckRepresentation(ctx, pool, ExternalRepresentationLink{freshCheck.ID, "representation-input", newer.Record.ID}); err != nil {
		t.Fatal(err)
	}
	old, err := RecordExternalRepresentation(ctx, pool, first)
	if err != nil || !old.Replayed || !reflect.DeepEqual(old.Record, a.Record) || old.Record.Definition.Dependencies[0].Status != "missing" {
		t.Fatal("old replay claimed current supplement", err)
	}
	oldCheck, err := ReadExternalCheck(ctx, pool, check.ID)
	if err != nil || !reflect.DeepEqual(oldCheck, check) {
		t.Fatal("supplement rewrote old check", err)
	}
	users, err = ReadExternalDependencyUsers(ctx, pool, first.Dependencies[0].Key, 10)
	if err != nil || len(users.Representations) != 2 || len(users.CheckInputs) != 2 {
		t.Fatal("history not discoverable", err)
	}
	limited, err := ReadExternalDependencyUsers(ctx, pool, first.Dependencies[0].Key, 1)
	if err != nil || !limited.RepresentationsTruncated || len(limited.Representations) != 1 {
		t.Fatal("representation truncation lost", err)
	}
	// Multiple checks for the single B representation exercise independent link truncation.
	for i := 0; i < 2; i++ {
		c := externalRepresentationCheck(t, ctx, pool, b.Record, fmt.Sprintf("check-b-%d", i))
		if _, err := LinkExternalCheckRepresentation(ctx, pool, ExternalRepresentationLink{c.ID, "representation-input", b.Record.ID}); err != nil {
			t.Fatal(err)
		}
	}
	limited, err = ReadExternalDependencyUsers(ctx, pool, second.Dependencies[0].Key, 1)
	if err != nil || limited.RepresentationsTruncated || !limited.CheckInputsTruncated || len(limited.CheckInputs) != 1 {
		t.Fatal("link truncation lost", err)
	}
	if reviewedSourceClaimAuthorityCounts(t, ctx, pool) != before {
		t.Fatal("annotation changed canonical/admission authority")
	}
	p, err := GetProposalByOccurrenceID(ctx, pool, subject.ProposalOccurrenceID)
	if err != nil || p.AdmissionOutcome != "pending" {
		t.Fatal("representation admitted claim", err)
	}
}

func TestIntegrationExternalRepresentationRejectsDriftWithZeroWrites(t *testing.T) {
	ctx, pool := integrationPool(t)
	subject := externalCheckTestSubject(t, ctx, pool, "subject", "1")
	other := externalCheckTestSubject(t, ctx, pool, "subject", "2")
	original := externalRepresentationFixture(subject, "original", "附件甲")
	first, err := RecordExternalRepresentation(ctx, pool, original)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ExternalRepresentationInput){
		"snapshot":    func(v *ExternalRepresentationInput) { v.Subject.SourceSnapshotID = other.SourceSnapshotID },
		"view":        func(v *ExternalRepresentationInput) { v.Subject.ExtractionViewID = other.ExtractionViewID },
		"version":     func(v *ExternalRepresentationInput) { v.Subject.SourceVersion = other.SourceVersion },
		"fingerprint": func(v *ExternalRepresentationInput) { v.Subject.ProposalFingerprint = "wrong" },
		"cross subject history": func(v *ExternalRepresentationInput) {
			v.Subject = other
			v.PreviousID = first.Record.ID
			v.RevisionReason = "wrong target"
		},
		"same request changed content": func(v *ExternalRepresentationInput) { v.RequestID = "original"; v.Version = "2" },
		"supplied mismatched view": func(v *ExternalRepresentationInput) {
			d := &v.Dependencies[0]
			d.Status = "supplied"
			d.SourceSnapshotID = subject.SourceSnapshotID
			d.ExtractionViewID = other.ExtractionViewID
			d.RenderedContentHash = subject.RenderedContentHash
		},
		"supplied wrong hash": func(v *ExternalRepresentationInput) {
			d := &v.Dependencies[0]
			d.Status = "supplied"
			d.SourceSnapshotID = subject.SourceSnapshotID
			d.ExtractionViewID = subject.ExtractionViewID
			d.RenderedContentHash = "wrong"
		},
		"unadmitted canonical target": func(v *ExternalRepresentationInput) { v.CanonicalNodeID = "node:absent" },
	} {
		t.Run(name, func(t *testing.T) {
			in := externalRepresentationFixture(subject, name, "附件甲")
			mutate(&in)
			if _, err := RecordExternalRepresentation(ctx, pool, in); err == nil {
				t.Fatal("drift accepted")
			}
			assertTableCount(t, ctx, pool, "external_representation_records", 1)
		})
	}
}

func TestIntegrationExternalRepresentationKeepsAlternativeDerivationsSeparate(t *testing.T) {
	ctx, pool := integrationPool(t)
	parentA := propositionTestClaim(t, ctx, pool, "parent-a")
	parentB := propositionTestClaim(t, ctx, pool, "parent-b")
	left := propositionTestClaim(t, ctx, pool, "claim-left", parentA)
	right := propositionTestClaim(t, ctx, pool, "claim-right", parentB)
	same := propositionTestKey("same-proposition")
	propositionTestBind(t, ctx, pool, left, "bind-left", same)
	propositionTestBind(t, ctx, pool, right, "bind-right", same)
	before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
	var proposalLeft, derivationLeft, derivationRight string
	if err := pool.QueryRow(ctx, `SELECT origin_proposal_occurrence_id,derivation_id FROM canonical_derivations WHERE node_id=$1`, left).Scan(&proposalLeft, &derivationLeft); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT derivation_id FROM canonical_derivations WHERE node_id=$1`, right).Scan(&derivationRight); err != nil {
		t.Fatal(err)
	}
	subject, err := LoadExternalCheckSubject(ctx, pool, proposalLeft)
	if err != nil {
		t.Fatal(err)
	}
	in := externalRepresentationFixture(subject, "left-representation", "附件甲")
	in.CanonicalNodeID = left
	in.DerivationID = derivationLeft
	got, err := RecordExternalRepresentation(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadExternalRepresentation(ctx, pool, got.Record.ID)
	if err != nil || read.Definition.DerivationID != derivationLeft || read.Definition.CanonicalNodeID != left {
		t.Fatal("lost exact derivation", err)
	}
	in.RequestID = "wrong-derivation"
	in.DerivationID = derivationRight
	if _, err := RecordExternalRepresentation(ctx, pool, in); err == nil {
		t.Fatal("same proposition allowed different derivation")
	}
	in.RequestID = "wrong-node"
	in.CanonicalNodeID = right
	if _, err := RecordExternalRepresentation(ctx, pool, in); err == nil {
		t.Fatal("same proposition allowed different origin")
	}
	assertTableCount(t, ctx, pool, "external_representation_records", 1)
	if reviewedSourceClaimAuthorityCounts(t, ctx, pool) != before {
		t.Fatal("representation altered evidence")
	}
	var parents []string
	rows, err := pool.Query(ctx, `SELECT parent_node_id FROM canonical_derivation_parents WHERE derivation_id=$1`, derivationLeft)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		parents = append(parents, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parents, []string{parentA}) {
		t.Fatal("alternative parents collapsed", parents)
	}
}

func TestIntegrationExternalRepresentationConcurrentRequestsAndLinks(t *testing.T) {
	ctx, pool := integrationPool(t)
	subject := externalCheckTestSubject(t, ctx, pool, "concurrent-representation", "1")
	in := externalRepresentationFixture(subject, "same-request", "附件甲")
	const n = 8
	start := make(chan struct{})
	receipts := make(chan ExternalRepresentationReceipt, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := RecordExternalRepresentation(ctx, pool, in)
			receipts <- r
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	firsts := 0
	var original ExternalRepresentationRecord
	for r := range receipts {
		if !r.Replayed {
			firsts++
		}
		if original.ID == "" {
			original = r.Record
		} else if !reflect.DeepEqual(original, r.Record) {
			t.Fatal("concurrent receipts differ")
		}
	}
	if firsts != 1 {
		t.Fatal("not exactly one write", firsts)
	}
	check := externalRepresentationCheck(t, ctx, pool, original, "concurrent-link")
	replayed := make(chan bool, n)
	errs = make(chan error, n)
	start = make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := LinkExternalCheckRepresentation(ctx, pool, ExternalRepresentationLink{check.ID, "representation-input", original.ID})
			replayed <- r
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(replayed)
	close(errs)
	firsts = 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for r := range replayed {
		if !r {
			firsts++
		}
	}
	if firsts != 1 {
		t.Fatal("not exactly one link", firsts)
	}
	// Race differing bodies under one new request: exactly one persists; no semantic winner is inferred.
	start = make(chan struct{})
	errs = make(chan error, 2)
	for _, version := range []string{"2", "3"} {
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			candidate := externalRepresentationFixture(subject, "competing-request", "附件甲")
			candidate.Version = v
			<-start
			_, err := RecordExternalRepresentation(ctx, pool, candidate)
			errs <- err
		}(version)
	}
	close(start)
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if strings.Contains(err.Error(), string(ErrorIdempotencyKeyReused)) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	assertTableCount(t, ctx, pool, "external_representation_records", 2)
	assertTableCount(t, ctx, pool, "external_check_representation_links", 1)
}

func TestIntegrationExternalRepresentationSQLGuardsAndRoles(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	subject := externalCheckTestSubject(t, ctx, pool, "role-representation", "1")
	writer := externalRecordRolePool(t, ctx, pool, dbrole.ProfileCoreRecords)
	reader := externalRecordRolePool(t, ctx, pool, dbrole.ProfileQuery)
	original := externalRepresentationFixture(subject, "original", "附件甲")
	r, err := RecordExternalRepresentation(ctx, writer, original)
	if err != nil {
		t.Fatal(err)
	}
	c := externalRepresentationCheck(t, ctx, writer, r.Record, "original-check")
	link := ExternalRepresentationLink{c.ID, "representation-input", r.Record.ID}
	if _, err := LinkExternalCheckRepresentation(ctx, writer, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExternalRepresentation(ctx, reader, r.Record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExternalDependencyUsers(ctx, reader, original.Dependencies[0].Key, 10); err != nil {
		t.Fatal(err)
	}
	original.RequestID = "reader-writes"
	if _, err := RecordExternalRepresentation(ctx, reader, original); err == nil {
		t.Fatal("query role wrote representation")
	}
	if _, err := LinkExternalCheckRepresentation(ctx, reader, link); err == nil {
		t.Fatal("query role wrote link")
	}
	for _, table := range []string{"external_representation_records", "external_check_representation_links"} {
		for _, sql := range []string{"UPDATE " + table + " SET " + map[string]string{"external_representation_records": "request_id=request_id", "external_check_representation_links": "input_id=input_id"}[table], "DELETE FROM " + table, "TRUNCATE " + table} {
			if _, err := pool.Exec(ctx, sql); err == nil {
				t.Fatal("owner changed history", sql)
			}
		}
	}
	for name, mutate := range map[string]func(*ExternalRepresentationInput){
		"wrong pointer ID": func(v *ExternalRepresentationInput) { v.Dependencies[0].Key.LocalID = "附件乙" },
		"negative array index": func(v *ExternalRepresentationInput) {
			v.Dependencies[0].Pointers = []string{"/condition/-1/dependency"}
		},
		"leading zero index": func(v *ExternalRepresentationInput) {
			v.Dependencies[0].Pointers = []string{"/condition/02/dependency"}
		},
		"duplicate JSON keys":   func(v *ExternalRepresentationInput) { v.Content = `{"x":"甲","x":"乙"}`; v.Dependencies = nil },
		"duplicate dependency":  func(v *ExternalRepresentationInput) { v.Dependencies = append(v.Dependencies, v.Dependencies[0]) },
		"missing with evidence": func(v *ExternalRepresentationInput) { v.Dependencies[0].SourceSnapshotID = subject.SourceSnapshotID },
		"supplied fake source": func(v *ExternalRepresentationInput) {
			d := &v.Dependencies[0]
			d.Status = "supplied"
			d.SourceSnapshotID = subject.SourceSnapshotID
			d.ExtractionViewID = subject.ExtractionViewID
			d.RenderedContentHash = "wrong"
		},
		"wrong subject": func(v *ExternalRepresentationInput) { v.Subject.SourceVersion = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			in := externalRepresentationFixture(subject, name, "附件甲")
			mutate(&in)
			body, e := json.Marshal(in)
			if e != nil {
				t.Fatal(e)
			}
			id := "representation:sha256:" + externalDigest(body)
			if _, err := writer.Exec(ctx, `INSERT INTO external_representation_records(representation_id,request_id,proposal_occurrence_id,body) VALUES($1,$2,$3,$4)`, id, in.RequestID, subject.ProposalOccurrenceID, string(body)); err == nil {
				t.Fatal("direct SQL accepted malformed representation")
			}
			assertTableCount(t, ctx, pool, "external_representation_records", 1)
		})
	}
	second, err := RecordExternalRepresentation(ctx, writer, externalRepresentationFixture(subject, "second", "附件乙"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `INSERT INTO external_check_representation_links(check_id,input_id,representation_id) VALUES($1,$2,$3)`, c.ID, "representation-input", second.Record.ID); err == nil {
		t.Fatal("SQL attached old check to changed declaration")
	}
	material, _ := r.Record.CheckMaterial("representation-input")
	otherSubject := externalCheckTestSubject(t, ctx, pool, "other-check-subject", "1")
	for name, mutate := range map[string]func(*ExternalCheckInput){
		"tree only":            func(v *ExternalCheckInput) { v.Materials[0].Content = r.Record.Definition.Content },
		"wrong role":           func(v *ExternalCheckInput) { v.Materials[0].Role = "candidate" },
		"wrong format version": func(v *ExternalCheckInput) { v.Materials[0].Version = "2" },
		"wrong mapping":        func(v *ExternalCheckInput) { v.Materials[0].MappingClaim = "unbound mapping" },
		"wrong subject":        func(v *ExternalCheckInput) { v.Subject = otherSubject },
	} {
		t.Run(name, func(t *testing.T) {
			in := externalCheckFixture(subject, name)
			in.Materials = []ExternalCheckMaterial{material}
			in.Findings[0].InputIDs = []string{material.ID}
			mutate(&in)
			check, err := RecordExternalCheck(ctx, writer, in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Exec(ctx, `INSERT INTO external_check_representation_links(check_id,input_id,representation_id) VALUES($1,$2,$3)`, check.Record.ID, material.ID, r.Record.ID); err == nil {
				t.Fatal("SQL accepted incompatible checker input")
			}
			assertTableCount(t, ctx, pool, "external_check_representation_links", 1)
		})
	}
	for _, table := range []string{"external_representation_records", "external_check_representation_links"} {
		t.Run("hidden "+table, func(t *testing.T) {
			if _, err := pool.Exec(ctx, "ALTER TABLE "+table+" ENABLE ROW LEVEL SECURITY"); err != nil {
				t.Fatal(err)
			}
			defer pool.Exec(ctx, "ALTER TABLE "+table+" DISABLE ROW LEVEL SECURITY")
			if _, err := ReadExternalDependencyUsers(ctx, reader, original.Dependencies[0].Key, 10); err == nil {
				t.Fatal("hidden history reported as complete")
			}
		})
	}
	down, err := os.ReadFile("../../migrations/000052_evidence_ingestion_external_representations.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("rollback erased history")
	}
	if _, err := ReadExternalRepresentation(ctx, reader, r.Record.ID); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationExternalRepresentationJSONPointerEscapes(t *testing.T) {
	ctx, pool := integrationPool(t)
	subject := externalCheckTestSubject(t, ctx, pool, "pointer-escapes", "1")
	for _, pointer := range []string{"/", "/a~1b/~0/0"} {
		t.Run(pointer, func(t *testing.T) {
			in := externalRepresentationFixture(subject, "pointer-"+pointer, "附件甲")
			id := in.Dependencies[0].Key.ID()
			if pointer == "/" {
				in.Content = `{"":"` + id + `"}`
			} else {
				in.Content = `{"a/b":{"~":["` + id + `"]}}`
			}
			in.Dependencies[0].Pointers = []string{pointer}
			r, err := RecordExternalRepresentation(ctx, pool, in)
			if err != nil {
				t.Fatal(err)
			}
			read, err := ReadExternalRepresentation(ctx, pool, r.Record.ID)
			if err != nil || read.Definition.Content != in.Content {
				t.Fatal("escaped path bytes changed", err)
			}
		})
	}
}

func TestIntegrationExternalRepresentationSQLKeyShape(t *testing.T) {
	ctx, pool := integrationPool(t)
	subject := externalCheckTestSubject(t, ctx, pool, "key-shape-probe", "1")
	input := externalRepresentationFixture(subject, "key-shape-probe", "附件甲")
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	dependency := object["dependencies"].([]any)[0].(map[string]any)
	dependency["key"].(map[string]any)["extra_identity_field"] = "silently-lost"
	body, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO external_representation_records(representation_id,request_id,proposal_occurrence_id,body) VALUES($1,$2,$3,$4)`, "representation:sha256:"+externalDigest(body), input.RequestID, subject.ProposalOccurrenceID, string(body))
	if err == nil {
		users, readErr := ReadExternalDependencyUsers(ctx, pool, input.Dependencies[0].Key, 10)
		t.Fatalf("unsupported identity shape accepted; reverse lookup found %d representations; read error=%v", len(users.Representations), readErr)
	}
}

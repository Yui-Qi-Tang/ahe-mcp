//go:build integration && registration_capacity

package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This explicit selection fills the real native catalog to its frozen byte
// boundary using only successful native source/admission/registration APIs.
// It is separate from ordinary integration selection because it creates
// thousands of independent synthetic source objects.
func TestIntegrationInitialRegistrationNativeCapacity(t *testing.T) {
	_, pool := initialRegistrationRoleDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	endpoint := initialRegistrationBoundedPool(t, ctx, pool, dbrole.ProfileEndpointReviewer)
	query := initialRegistrationBoundedPool(t, ctx, pool, dbrole.ProfileQuery)
	var firstKey, firstClaim string
	var count, size int64
	usage := func() {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT count(*),2+coalesce(sum(octet_length('{"event_id":"'||event_id||'","payload":'||body||'}')),0)+greatest(count(*)-1,0) FROM canonical_initial_lineage_registrations`).Scan(&count, &size); err != nil {
			t.Fatal(err)
		}
	}
	usage()
	for i := 0; size < evidencesupersession.MaxRegistrationCatalogBytes && i < evidencesupersession.MaxRegistrationEvents; i++ {
		label := fmt.Sprintf("capacity-%06d", i)
		in := registrationNativeFixture(t, ctx, pool, label, "OBJECT-"+label)
		review, err := LoadInitialLineageRegistrationReview(ctx, pool, in.Review)
		if err != nil {
			t.Fatal(err)
		}
		payload := evidencesupersession.InitialRegistrationPayload{ContractVersion: evidencesupersession.InitialRegistrationContractV1, Binding: review.Binding, ReviewSubjectID: review.Subject, RequestID: in.RequestID, Reviewer: in.ReviewerID, Reason: "x"}
		id, err := payload.ID()
		if err != nil {
			t.Fatal(err)
		}
		minimum, err := json.Marshal(evidencesupersession.RegistrationRecord{ID: id, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		minSize := int64(len(minimum))
		maxSize := minSize + 1999
		left := int64(evidencesupersession.MaxRegistrationCatalogBytes) - size
		if count > 0 {
			left--
		}
		chosen := maxSize
		if left <= maxSize {
			chosen = left
		} else {
			remaining := left - maxSize - 1
			if remaining < minSize {
				chosen = left - minSize - 1
			} else if remaining > maxSize && remaining < 2*minSize+1 {
				chosen -= 2*minSize + 1 - remaining
			}
		}
		if chosen < minSize || chosen > maxSize {
			t.Fatalf("cannot construct exact byte boundary with unchanged canonical fields: left=%d min=%d max=%d chosen=%d", left, minSize, maxSize, chosen)
		}
		in.Reason = strings.Repeat("x", int(chosen-minSize)+1)
		r, err := ApplyInitialLineageRegistration(ctx, pool, in)
		if err != nil {
			t.Fatal("native capacity append", i, err)
		}
		if firstKey == "" {
			firstKey = r.Record.Payload.Binding.LineageKey
			firstClaim = in.Review.ClaimID
		}
		previous := size
		usage()
		separator := int64(0)
		if count > 1 {
			separator = 1
		}
		if size != previous+chosen+separator {
			t.Fatalf("catalog byte accounting drift: got %d expected %d", size, previous+chosen+separator)
		}
		if i%500 == 0 || size == evidencesupersession.MaxRegistrationCatalogBytes {
			t.Logf("native capacity progress: records=%d bytes=%d/%d", count, size, evidencesupersession.MaxRegistrationCatalogBytes)
		}
		if count == 2000 {
			registrationCapacityDerivedRoundTrip(t, ctx, pool, endpoint, query, "2000", firstClaim)
		}
	}
	if size != evidencesupersession.MaxRegistrationCatalogBytes {
		t.Fatalf("did not reach exact native byte boundary: count=%d size=%d", count, size)
	}
	beforeCount, beforeSize := count, size
	in := registrationNativeFixture(t, ctx, pool, "capacity-overflow", "OBJECT-capacity-overflow")
	before := registrationBaselineCounts(t, ctx, pool)
	if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err == nil || !strings.Contains(err.Error(), "capacity exceeded") {
		t.Fatalf("native overflow rejection=%v", err)
	}
	if after := registrationBaselineCounts(t, ctx, pool); !reflect.DeepEqual(before, after) {
		t.Fatal("capacity rejection left partial lineage/membership/event")
	}
	usage()
	if count != beforeCount || size != beforeSize {
		t.Fatal("capacity rejection changed catalog")
	}
	p, err := GetJointLineageCurrentness(ctx, pool, firstKey)
	if err != nil || !p.ClosureAvailable || p.Witness == nil {
		t.Fatal("existing closure unreadable at full catalog", err)
	}
	registrationCapacityDerivedRoundTrip(t, ctx, pool, endpoint, query, "full", firstClaim)
	t.Logf("native exact byte bound PASS: count=%d size=%d; extra append rolled back; original closure readable; independent count K=%d checked in domain boundary suite", count, size, evidencesupersession.MaxRegistrationEvents)
}

// Each operation has the shipping MCP's 60-second budget. This covers the
// complete reviewed derived path under actual bounded roles, not only one
// lineage projection. Every invocation uses a fresh transaction/cut.
func registrationCapacityDerivedRoundTrip(t *testing.T, ctx context.Context, owner, endpoint, query *pgxpool.Pool, label, parent string) {
	t.Helper()
	p := createIntegrationClosureExternalProposal(t, ctx, owner, "capacity-derived-"+label, "CAPACITY-DERIVED-"+label, "r1", "Synthetic capacity AND conclusion.")
	request := registrationCurrentEndpointRequest(p.ProposalOccurrenceID, parent)
	operation := func(name string, run func(context.Context) error) {
		t.Helper()
		operationCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		started := time.Now()
		if err := run(operationCtx); err != nil {
			t.Fatalf("capacity %s %s failed after %s: %v", label, name, time.Since(started), err)
		}
		if operationCtx.Err() != nil {
			t.Fatalf("capacity %s %s exceeded its budget", label, name)
		}
		t.Logf("capacity %s %s elapsed=%s", label, name, time.Since(started))
	}
	var review EndpointReview
	operation("derived review", func(ctx context.Context) (err error) {
		review, err = LoadEndpointReview(ctx, endpoint, request)
		return err
	})
	input := ReviewedEndpointAdmissionInput{RequestID: "capacity-derived-" + label, Review: request, ExpectedSubject: review.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic AND capacity acceptance"}
	var receipt EndpointAdmissionReceipt
	operation("derived commit", func(ctx context.Context) (err error) {
		receipt, err = AdmitReviewedEndpoint(ctx, endpoint, input)
		return err
	})
	if receipt.Admission.Replayed || len(receipt.Admission.ParentNodeIDs) != 1 || receipt.Admission.ParentNodeIDs[0] != parent {
		t.Fatal("fresh derived admission or parents changed", receipt)
	}
	operation("exact retry", func(ctx context.Context) error {
		again, err := AdmitReviewedEndpoint(ctx, endpoint, input)
		if err != nil {
			return err
		}
		want := receipt
		want.Admission.Replayed = true
		if !reflect.DeepEqual(again, want) {
			return fmt.Errorf("derived terminal receipt changed")
		}
		return nil
	})
	operation("current-use readback", func(ctx context.Context) error {
		p, err := ReadEvidenceUseProjectionForPolicy(ctx, query, evidenceuse.CurrentActionV1, []string{receipt.Admission.CanonicalRef})
		if err != nil {
			return err
		}
		for _, n := range p.Assessment.Nodes {
			if n.NodeID == receipt.Admission.CanonicalRef && n.Outcome == evidenceuse.Eligible {
				return nil
			}
		}
		return fmt.Errorf("new derived claim did not pass complete current-use assessment")
	})
}

package evidenceingestion

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestEndpointReviewRequestContract(t *testing.T) {
	valid := EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: "occ:synthetic",
		Derivation: &EndpointDerivation{UsePolicy: "current-action/v1", ParentNodeIDs: []string{"canon-node:a", "canon-node:b"}, Method: "AND", Producer: "synthetic", TraceRef: "synthetic:trace"}}
	if err := validateEndpointRequest(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*EndpointReviewRequest){
		func(v *EndpointReviewRequest) { v.Kind = "source_claim" },
		func(v *EndpointReviewRequest) { v.ProposalOccurrenceID = "" },
		func(v *EndpointReviewRequest) { v.Derivation = nil },
		func(v *EndpointReviewRequest) { v.Derivation.ParentNodeIDs = []string{"canon-node:b", "canon-node:a"} },
		func(v *EndpointReviewRequest) { v.Derivation.ParentNodeIDs = []string{"canon-node:a", "canon-node:a"} },
		func(v *EndpointReviewRequest) { v.Derivation.ParentNodeIDs = []string{"occ:pending"} },
		func(v *EndpointReviewRequest) { v.Derivation.Method = "" },
		func(v *EndpointReviewRequest) { v.Derivation.TraceRef = " vague" },
		func(v *EndpointReviewRequest) { v.Derivation.Producer = strings.Repeat("x", 201) },
		func(v *EndpointReviewRequest) { v.Derivation.UsePolicy = "historical" },
		func(v *EndpointReviewRequest) { v.Kind = "repository_code" },
	} {
		copy := valid
		d := *valid.Derivation
		copy.Derivation = &d
		mutate(&copy)
		if err := validateEndpointRequest(copy); err == nil {
			t.Fatalf("invalid endpoint accepted: %+v", copy)
		}
	}
	code := EndpointReviewRequest{Kind: "repository_code", ProposalOccurrenceID: "occ:synthetic"}
	if err := validateEndpointRequest(code); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointHistoricalRequestDoesNotAcquireUsePolicyOnDecode(t *testing.T) {
	// A stored request from before governance policy selection must keep its old
	// JSON shape. New pending admission separately rejects this omitted purpose.
	const historical = `{"kind":"derived_spec","proposal_occurrence_id":"occ:synthetic","derivation":{"parent_node_ids":["canon-node:a"],"method":"AND","producer":"synthetic","trace_ref":"synthetic:trace"}}`
	var request EndpointReviewRequest
	if err := json.Unmarshal([]byte(historical), &request); err != nil {
		t.Fatal(err)
	}
	if err := validateEndpointRequest(request); err != nil {
		t.Fatal("historical request cannot enter exact replay", err)
	}
	raw, err := json.Marshal(request)
	if err != nil || strings.Contains(string(raw), "use_policy") || request.Derivation.UsePolicy != "" {
		t.Fatal("historical subject acquired a new purpose", string(raw), err)
	}
}

func TestEndpointReviewParentCountBoundaries(t *testing.T) {
	for _, count := range []int{0, 1, 8, 9, 12, 32, 33} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			parents := make([]string, count)
			for i := range parents {
				parents[i] = fmt.Sprintf("canon-node:parent-%02d", i)
			}
			req := EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: "occ:synthetic",
				Derivation: &EndpointDerivation{UsePolicy: "current-action/v1", ParentNodeIDs: parents, Method: "AND", Producer: "synthetic", TraceRef: "synthetic:trace"}}
			err := validateEndpointRequest(req)
			if count == 0 || count == 33 {
				assertKind(t, err, ErrorInvalidInput)
			} else if err != nil {
				t.Fatalf("%d sorted admitted-coordinate parents rejected: %v", count, err)
			}
		})
	}
}

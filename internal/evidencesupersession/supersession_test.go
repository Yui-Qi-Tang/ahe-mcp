package evidencesupersession

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
)

func TestBasisNormalizeValidateAndKey(t *testing.T) {
	original := Basis{
		SourceSystem:    " jira ",
		SourceNamespace: " tenant:acme ",
		ObjectType:      " issue ",
		ObjectID:        " PAY-42 ",
		SlotKind:        " field ",
		SlotID:          " description ",
	}
	basis := original.Normalize()
	if basis.SourceSystem != "jira" || basis.SourceNamespace != "tenant:acme" ||
		basis.ObjectType != "issue" || basis.ObjectID != "PAY-42" ||
		basis.SlotKind != "field" || basis.SlotID != "description" {
		t.Fatalf("Normalize() = %+v", basis)
	}
	if original.SourceSystem != " jira " || original.SlotID != " description " {
		t.Fatalf("Normalize() mutated receiver source: %+v", original)
	}
	if err := basis.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	first, err := basis.Key()
	if err != nil {
		t.Fatalf("first Key() error = %v", err)
	}
	second, err := basis.Key()
	if err != nil {
		t.Fatalf("second Key() error = %v", err)
	}
	if first != second {
		t.Fatalf("Key() = %q then %q", first, second)
	}
	const want = "lineage:v1:sha256:06a00873810971d01f55e97ed35c7f3544e733128b3853ca329211ef35e8d406"
	if first != want {
		t.Fatalf("Key() = %q, want %q", first, want)
	}

	variants := []Basis{
		{SourceSystem: "jira-2", SourceNamespace: basis.SourceNamespace, ObjectType: basis.ObjectType, ObjectID: basis.ObjectID, SlotKind: basis.SlotKind, SlotID: basis.SlotID},
		{SourceSystem: basis.SourceSystem, SourceNamespace: "tenant:other", ObjectType: basis.ObjectType, ObjectID: basis.ObjectID, SlotKind: basis.SlotKind, SlotID: basis.SlotID},
		{SourceSystem: basis.SourceSystem, SourceNamespace: basis.SourceNamespace, ObjectType: "page", ObjectID: basis.ObjectID, SlotKind: basis.SlotKind, SlotID: basis.SlotID},
		{SourceSystem: basis.SourceSystem, SourceNamespace: basis.SourceNamespace, ObjectType: basis.ObjectType, ObjectID: "PAY-43", SlotKind: basis.SlotKind, SlotID: basis.SlotID},
		{SourceSystem: basis.SourceSystem, SourceNamespace: basis.SourceNamespace, ObjectType: basis.ObjectType, ObjectID: basis.ObjectID, SlotKind: "comment", SlotID: basis.SlotID},
		{SourceSystem: basis.SourceSystem, SourceNamespace: basis.SourceNamespace, ObjectType: basis.ObjectType, ObjectID: basis.ObjectID, SlotKind: basis.SlotKind, SlotID: "summary"},
	}
	for _, variant := range variants {
		key, err := variant.Key()
		if err != nil {
			t.Fatalf("variant Key() error = %v", err)
		}
		if key == first {
			t.Fatalf("variant %+v retained lineage key %q", variant, key)
		}
	}
}

func TestBasisValidateRejectsMalformedFields(t *testing.T) {
	valid := testBasis()
	tests := []struct {
		name   string
		mutate func(*Basis)
	}{
		{name: "missing source system", mutate: func(value *Basis) { value.SourceSystem = "" }},
		{name: "invalid source system token", mutate: func(value *Basis) { value.SourceSystem = "Jira Cloud" }},
		{name: "missing namespace", mutate: func(value *Basis) { value.SourceNamespace = "" }},
		{name: "missing object type", mutate: func(value *Basis) { value.ObjectType = "" }},
		{name: "missing object ID", mutate: func(value *Basis) { value.ObjectID = "" }},
		{name: "missing slot kind", mutate: func(value *Basis) { value.SlotKind = "" }},
		{name: "missing slot ID", mutate: func(value *Basis) { value.SlotID = "" }},
		{name: "surrounding whitespace", mutate: func(value *Basis) { value.SlotID = " description" }},
		{name: "invalid UTF-8", mutate: func(value *Basis) { value.ObjectID = string([]byte{0xff}) }},
		{name: "long source system", mutate: func(value *Basis) { value.SourceSystem = strings.Repeat("a", maxSourceSystemBytes+1) }},
		{name: "long namespace", mutate: func(value *Basis) { value.SourceNamespace = strings.Repeat("a", maxSourceNamespaceBytes+1) }},
		{name: "long object type", mutate: func(value *Basis) { value.ObjectType = strings.Repeat("a", maxObjectTypeBytes+1) }},
		{name: "long object ID", mutate: func(value *Basis) { value.ObjectID = strings.Repeat("a", maxObjectIDBytes+1) }},
		{name: "long slot kind", mutate: func(value *Basis) { value.SlotKind = strings.Repeat("a", maxSlotKindBytes+1) }},
		{name: "long slot ID", mutate: func(value *Basis) { value.SlotID = strings.Repeat("a", maxSlotIDBytes+1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			if err := value.Validate(); !errors.Is(err, ErrInvalidBasis) {
				t.Fatalf("Validate() error = %v, want ErrInvalidBasis", err)
			}
			if _, err := value.Key(); !errors.Is(err, ErrInvalidBasis) {
				t.Fatalf("Key() error = %v, want ErrInvalidBasis", err)
			}
		})
	}
}

func TestNormalizeTargetsCopiesSortsAndRejectsInvalidSets(t *testing.T) {
	input := []string{" canon-node:old-b ", "canon-node:old-a"}
	got, err := NormalizeTargets(input)
	if err != nil {
		t.Fatalf("NormalizeTargets() error = %v", err)
	}
	want := []string{"canon-node:old-a", "canon-node:old-b"}
	if !slices.Equal(got, want) {
		t.Fatalf("NormalizeTargets() = %v, want %v", got, want)
	}
	if input[0] != " canon-node:old-b " {
		t.Fatalf("NormalizeTargets() mutated input = %v", input)
	}

	tests := []struct {
		name   string
		values []string
	}{
		{name: "empty"},
		{name: "duplicate after normalization", values: []string{"canon-node:a", " canon-node:a "}},
		{name: "invalid prefix", values: []string{"node:a"}},
		{name: "empty suffix", values: []string{"canon-node:"}},
		{name: "invalid UTF-8", values: []string{"canon-node:" + string([]byte{0xff})}},
		{name: "over limit", values: repeatedNodeIDs(MaxReplacementTargets + 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeTargets(test.values); !errors.Is(err, ErrInvalidTargets) {
				t.Fatalf("NormalizeTargets() error = %v, want ErrInvalidTargets", err)
			}
		})
	}
}

func TestRequestPayloadV2CanonicalHash(t *testing.T) {
	basisInput := Basis{
		SourceSystem:    " jira ",
		SourceNamespace: " tenant:acme ",
		ObjectType:      " issue ",
		ObjectID:        " PAY-42 ",
		SlotKind:        " field ",
		SlotID:          " description ",
	}
	targetsInput := []string{"canon-node:old-b", " canon-node:old-a "}
	payload, err := NewRequestPayloadV2(
		" occ:proposal ",
		" canon-node:new ",
		basisInput,
		targetsInput,
		0,
		"",
	)
	if err != nil {
		t.Fatalf("NewRequestPayloadV2() error = %v", err)
	}
	if payload.ContractVersion != RequestContractVersionV2 ||
		payload.ProposalOccurrence != "occ:proposal" ||
		payload.ReplacementNodeID != "canon-node:new" ||
		!slices.Equal(payload.TargetNodeIDs, []string{"canon-node:old-a", "canon-node:old-b"}) {
		t.Fatalf("request payload = %+v", payload)
	}
	if targetsInput[1] != " canon-node:old-a " || basisInput.SourceSystem != " jira " {
		t.Fatal("NewRequestPayloadV2() mutated caller input")
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	hash, err := payload.Hash()
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	const want = "sha256:1fc5d6a1ea233eba4d430d7b389e6869141f758be39b386bbd9deeede60ecb4a"
	if hash != want {
		t.Fatalf("Hash() = %q, want %q", hash, want)
	}

	mutated := payload
	mutated.TargetNodeIDs = []string{"canon-node:old-b", "canon-node:old-a"}
	if _, err := mutated.Hash(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unordered Hash() error = %v, want ErrInvalidPayload", err)
	}
	mutated = payload
	mutated.LineageKey = "lineage:v1:sha256:" + strings.Repeat("0", 64)
	if _, err := mutated.Hash(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("wrong lineage Hash() error = %v, want ErrInvalidPayload", err)
	}
	mutated = payload
	mutated.ContractVersion = "supersession-admission-request/v1"
	if _, err := mutated.Hash(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("wrong contract Hash() error = %v, want ErrInvalidPayload", err)
	}
}

func TestRequestPayloadV2RejectsInvalidHeadAndSelfTarget(t *testing.T) {
	tests := []struct {
		name     string
		revision int64
		head     string
		targets  []string
	}{
		{name: "negative revision", revision: -1, targets: []string{"canon-node:old"}},
		{name: "revision zero with head", head: testEventID("a"), targets: []string{"canon-node:old"}},
		{name: "positive revision without head", revision: 1, targets: []string{"canon-node:old"}},
		{name: "positive revision with malformed head", revision: 1, head: "admission-event:v1:sha256:" + strings.Repeat("a", 64), targets: []string{"canon-node:old"}},
		{name: "self target", targets: []string{"canon-node:new"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRequestPayloadV2(
				"occ:proposal",
				"canon-node:new",
				testBasis(),
				test.targets,
				test.revision,
				test.head,
			)
			if !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("NewRequestPayloadV2() error = %v, want ErrInvalidPayload", err)
			}
		})
	}

	valid, err := NewRequestPayloadV2(
		"occ:proposal",
		"canon-node:new",
		testBasis(),
		[]string{"canon-node:old"},
		1,
		testEventID("a"),
	)
	if err != nil || valid.ExpectedRevision != 1 {
		t.Fatalf("valid positive-head request = %+v, error = %v", valid, err)
	}
}

func TestDecisionPayloadV2CanonicalHash(t *testing.T) {
	payload, err := NewDecisionPayloadV2(
		" occ:proposal ",
		" canon-node:new ",
		" yuki ",
		" reviewed replacement ",
	)
	if err != nil {
		t.Fatalf("NewDecisionPayloadV2() error = %v", err)
	}
	if payload.Outcome != "admitted" || payload.DecisionBy != "yuki" || payload.DecisionReason != "reviewed replacement" {
		t.Fatalf("decision payload = %+v", payload)
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	hash, err := payload.Hash()
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	const want = "sha256:88775a1a68a1d8b1b71472114f3435ab5288bd0758e68bfa99d00fc6e46b28d0"
	if hash != want {
		t.Fatalf("Hash() = %q, want %q", hash, want)
	}
	changedReviewer, err := NewDecisionPayloadV2(
		payload.ProposalOccurrence,
		payload.CanonicalRef,
		"other",
		payload.DecisionReason,
	)
	if err != nil {
		t.Fatalf("NewDecisionPayloadV2(changed reviewer) error = %v", err)
	}
	changedHash, err := changedReviewer.Hash()
	if err != nil {
		t.Fatalf("changed reviewer Hash() error = %v", err)
	}
	if changedHash == hash {
		t.Fatalf("changed reviewer retained decision hash %q", changedHash)
	}

	for name, mutate := range map[string]func(*DecisionPayloadV2){
		"contract":   func(value *DecisionPayloadV2) { value.ContractVersion = "v1" },
		"outcome":    func(value *DecisionPayloadV2) { value.Outcome = "rejected" },
		"whitespace": func(value *DecisionPayloadV2) { value.DecisionReason = " reviewed replacement" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := payload
			mutate(&changed)
			if _, err := changed.Hash(); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("Hash() error = %v, want ErrInvalidPayload", err)
			}
		})
	}
}

func TestDecisionPayloadV2RejectsInvalidAudit(t *testing.T) {
	tests := []struct {
		name     string
		proposal string
		node     string
		by       string
		reason   string
	}{
		{name: "bad proposal", proposal: "proposal:1", node: "canon-node:new", by: "yuki", reason: "reviewed"},
		{name: "bad node", proposal: "occ:1", node: "node:new", by: "yuki", reason: "reviewed"},
		{name: "missing reviewer", proposal: "occ:1", node: "canon-node:new", reason: "reviewed"},
		{name: "missing reason", proposal: "occ:1", node: "canon-node:new", by: "yuki"},
		{name: "invalid reviewer UTF-8", proposal: "occ:1", node: "canon-node:new", by: string([]byte{0xff}), reason: "reviewed"},
		{name: "long reviewer", proposal: "occ:1", node: "canon-node:new", by: strings.Repeat("r", maxDecisionByBytes+1), reason: "reviewed"},
		{name: "long reason", proposal: "occ:1", node: "canon-node:new", by: "yuki", reason: strings.Repeat("r", maxDecisionReasonBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewDecisionPayloadV2(test.proposal, test.node, test.by, test.reason)
			if !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("NewDecisionPayloadV2() error = %v, want ErrInvalidPayload", err)
			}
		})
	}
}

func TestAtomicReplacementEventV2CanonicalID(t *testing.T) {
	request := mustRequest(t, 0, "")
	decision := mustDecision(t)
	bootstrapInput := []string{"canon-node:old-b"}
	event, err := NewAtomicReplacementEventV2(request, decision, " adm:decision ", bootstrapInput)
	if err != nil {
		t.Fatalf("NewAtomicReplacementEventV2() error = %v", err)
	}
	if event.ContractVersion != AdmissionEventContractVersionV2 || event.Kind != AtomicReplacementEventKind ||
		event.PreviousRevision != 0 || event.PreviousEventID != "" || event.Revision != 1 ||
		!slices.Equal(event.BootstrappedTargetNodeIDs, []string{"canon-node:old-b"}) {
		t.Fatalf("event = %+v", event)
	}
	if bootstrapInput[0] != "canon-node:old-b" {
		t.Fatal("NewAtomicReplacementEventV2() mutated bootstrap input")
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	id, err := event.ID()
	if err != nil {
		t.Fatalf("ID() error = %v", err)
	}
	const want = "admission-event:v2:sha256:fe25a3b1bbc9676e3140be44eb020b5024ee2f967d13c35c8e1b7b36cb0b32d2"
	if id != want {
		t.Fatalf("ID() = %q, want %q", id, want)
	}
	second, err := event.ID()
	if err != nil || second != id {
		t.Fatalf("second ID() = %q, %v; want %q", second, err, id)
	}
	if !strings.HasPrefix(id, "admission-event:v2:sha256:") || len(id) != len("admission-event:v2:sha256:")+64 {
		t.Fatalf("ID() shape = %q", id)
	}
}

func TestAtomicReplacementEventV2RejectsMismatchedDecisionAndBootstrap(t *testing.T) {
	request := mustRequest(t, 0, "")
	decision := mustDecision(t)

	wrongProposal, err := NewDecisionPayloadV2("occ:other", "canon-node:new", "yuki", "reviewed replacement")
	if err != nil {
		t.Fatalf("build wrong proposal decision: %v", err)
	}
	wrongNode, err := NewDecisionPayloadV2("occ:proposal", "canon-node:other", "yuki", "reviewed replacement")
	if err != nil {
		t.Fatalf("build wrong node decision: %v", err)
	}
	tests := []struct {
		name       string
		decision   DecisionPayloadV2
		decisionID string
		bootstrap  []string
	}{
		{name: "wrong decision proposal", decision: wrongProposal, decisionID: "adm:decision"},
		{name: "wrong decision node", decision: wrongNode, decisionID: "adm:decision"},
		{name: "bad decision ID", decision: decision, decisionID: "decision:1"},
		{name: "unknown target", decision: decision, decisionID: "adm:decision", bootstrap: []string{"canon-node:unknown"}},
		{name: "replacement is not a target", decision: decision, decisionID: "adm:decision", bootstrap: []string{"canon-node:new"}},
		{name: "duplicate target", decision: decision, decisionID: "adm:decision", bootstrap: []string{"canon-node:old-a", "canon-node:old-a"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewAtomicReplacementEventV2(request, test.decision, test.decisionID, test.bootstrap)
			if !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("NewAtomicReplacementEventV2() error = %v, want ErrInvalidPayload", err)
			}
		})
	}
}

func TestAdmissionEventPayloadV2ValidateRejectsEveryAuthorityMutation(t *testing.T) {
	event, err := NewAtomicReplacementEventV2(
		mustRequest(t, 1, testEventID("a")),
		mustDecision(t),
		"adm:decision",
		[]string{"canon-node:old-a"},
	)
	if err != nil {
		t.Fatalf("NewAtomicReplacementEventV2() error = %v", err)
	}
	originalID, err := event.ID()
	if err != nil {
		t.Fatalf("original ID() error = %v", err)
	}

	validMutations := map[string]func(*AdmissionEventPayloadV2){
		"decision hash": func(value *AdmissionEventPayloadV2) { value.DecisionPayloadHash = testHash("c") },
		"decision ID":   func(value *AdmissionEventPayloadV2) { value.AdmissionDecisionID = "adm:other" },
		"bootstrap set": func(value *AdmissionEventPayloadV2) {
			value.BootstrappedTargetNodeIDs = []string{"canon-node:old-b"}
		},
	}
	for name, mutate := range validMutations {
		t.Run("identity changes for "+name, func(t *testing.T) {
			changed := cloneEvent(event)
			mutate(&changed)
			changedID, err := changed.ID()
			if err != nil {
				t.Fatalf("changed ID() error = %v", err)
			}
			if changedID == originalID {
				t.Fatalf("%s retained event ID %q", name, changedID)
			}
		})
	}

	invalidMutations := map[string]func(*AdmissionEventPayloadV2){
		"contract":    func(value *AdmissionEventPayloadV2) { value.ContractVersion = "supersession-admission-event/v1" },
		"kind":        func(value *AdmissionEventPayloadV2) { value.Kind = "edge_backfill" },
		"proposal":    func(value *AdmissionEventPayloadV2) { value.ProposalOccurrence = "proposal:1" },
		"replacement": func(value *AdmissionEventPayloadV2) { value.ReplacementNodeID = "node:new" },
		"targets order": func(value *AdmissionEventPayloadV2) {
			value.TargetNodeIDs = []string{"canon-node:old-b", "canon-node:old-a"}
		},
		"basis": func(value *AdmissionEventPayloadV2) { value.LineageBasis.SlotID = "other" },
		"lineage key": func(value *AdmissionEventPayloadV2) {
			value.LineageKey = "lineage:v1:sha256:" + strings.Repeat("0", 64)
		},
		"previous head": func(value *AdmissionEventPayloadV2) { value.PreviousEventID = "" },
		"revision gap":  func(value *AdmissionEventPayloadV2) { value.Revision++ },
		"mismatched request hash": func(value *AdmissionEventPayloadV2) {
			value.RequestPayloadHash = testHash("b")
		},
		"bad request hash": func(value *AdmissionEventPayloadV2) { value.RequestPayloadHash = "sha256:bad" },
		"bad decision hash": func(value *AdmissionEventPayloadV2) {
			value.DecisionPayloadHash = strings.ToUpper(value.DecisionPayloadHash)
		},
		"unknown bootstrap": func(value *AdmissionEventPayloadV2) {
			value.BootstrappedTargetNodeIDs = []string{"canon-node:unknown"}
		},
	}
	for name, mutate := range invalidMutations {
		t.Run("rejects "+name, func(t *testing.T) {
			changed := cloneEvent(event)
			mutate(&changed)
			if _, err := changed.ID(); err == nil {
				t.Fatal("ID() error = nil, want rejection")
			}
		})
	}
}

func TestAtomicReplacementEventV2RejectsRevisionOverflow(t *testing.T) {
	request := mustRequest(t, math.MaxInt64, testEventID("a"))
	_, err := NewAtomicReplacementEventV2(
		request,
		mustDecision(t),
		"adm:decision",
		nil,
	)
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("NewAtomicReplacementEventV2() error = %v, want ErrInvalidPayload", err)
	}
}

func testBasis() Basis {
	return Basis{
		SourceSystem:    "jira",
		SourceNamespace: "tenant:acme",
		ObjectType:      "issue",
		ObjectID:        "PAY-42",
		SlotKind:        "field",
		SlotID:          "description",
	}
}

func mustRequest(t *testing.T, revision int64, head string) RequestPayloadV2 {
	t.Helper()
	payload, err := NewRequestPayloadV2(
		"occ:proposal",
		"canon-node:new",
		testBasis(),
		[]string{"canon-node:old-b", "canon-node:old-a"},
		revision,
		head,
	)
	if err != nil {
		t.Fatalf("NewRequestPayloadV2() error = %v", err)
	}
	return payload
}

func mustDecision(t *testing.T) DecisionPayloadV2 {
	t.Helper()
	payload, err := NewDecisionPayloadV2(
		"occ:proposal",
		"canon-node:new",
		"yuki",
		"reviewed replacement",
	)
	if err != nil {
		t.Fatalf("NewDecisionPayloadV2() error = %v", err)
	}
	return payload
}

func repeatedNodeIDs(count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = "canon-node:" + strings.Repeat("x", index+1)
	}
	return values
}

func testHash(char string) string {
	return "sha256:" + strings.Repeat(char, 64)
}

func testEventID(char string) string {
	return "admission-event:v2:" + testHash(char)
}

func cloneEvent(event AdmissionEventPayloadV2) AdmissionEventPayloadV2 {
	event.TargetNodeIDs = append([]string(nil), event.TargetNodeIDs...)
	event.BootstrappedTargetNodeIDs = append([]string(nil), event.BootstrappedTargetNodeIDs...)
	return event
}

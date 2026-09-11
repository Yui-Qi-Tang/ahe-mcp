package evidenceingestion

import (
	"context"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func TestMockSQLAdmitPendingProposalReplayRejectsChangedAuditMetadata(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	ingested, err := ingestManualText(ctx, db, testManualInput("mock-admission-replay-audit"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	input := AdmissionInput{
		ProposalOccurrenceID: ingested.ProposalOccurrenceID,
		DecisionBy:           "reviewer-a",
		DecisionReason:       "reviewed against source excerpt",
	}
	if _, err := admitPendingProposal(ctx, db, input); err != nil {
		t.Fatalf("first admitPendingProposal() error = %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*AdmissionInput)
	}{
		{
			name: "reviewer",
			mutate: func(input *AdmissionInput) {
				input.DecisionBy = "reviewer-b"
			},
		},
		{
			name: "reason",
			mutate: func(input *AdmissionInput) {
				input.DecisionReason = "different review basis"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replayInput := input
			tc.mutate(&replayInput)
			_, err := admitPendingProposal(ctx, db, replayInput)
			assertKind(t, err, ErrorAdmissionReplayConflict)
		})
	}
}

func TestCanonicalGraphNodeRowMatchesAllowsOnlyOriginProposalDifference(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	expected := canonicalGraphNodeFixture(t)
	actual := expected
	actual.OriginProposalOccurrenceID = "occ:first-materializer"
	db.canonicalGraphNodes[actual.ID] = actual
	originProposalID, matches, err := canonicalGraphNodeRowMatches(ctx, db, expected)
	if err != nil || !matches || originProposalID != actual.OriginProposalOccurrenceID {
		t.Fatalf("exact semantic node match = origin %q matches %t error %v", originProposalID, matches, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*CanonicalGraphNode)
	}{
		{name: "kind", mutate: func(got *CanonicalGraphNode) { got.Kind = evidencegraph.CanonicalDerivedClaim }},
		{name: "payload", mutate: func(got *CanonicalGraphNode) { got.Payload.Claim = "drift" }},
		{name: "producer", mutate: func(got *CanonicalGraphNode) { got.Provenance.Producer = "drift" }},
		{name: "method", mutate: func(got *CanonicalGraphNode) { got.Provenance.Method = "drift" }},
		{name: "method version", mutate: func(got *CanonicalGraphNode) { got.Provenance.MethodVersion = "v2" }},
		{name: "trace", mutate: func(got *CanonicalGraphNode) { got.Provenance.TraceRef = "drift" }},
		{name: "temporal", mutate: func(got *CanonicalGraphNode) { got.Temporal.Status = evidencegraph.TemporalCurrent }},
		{name: "integrity", mutate: func(got *CanonicalGraphNode) { got.Integrity.Digest = "drift" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := expected
			test.mutate(&got)
			db.canonicalGraphNodes[expected.ID] = got
			_, matches, err := canonicalGraphNodeRowMatches(ctx, db, expected)
			if err != nil {
				t.Fatalf("canonicalGraphNodeRowMatches() error = %v", err)
			}
			if matches {
				t.Fatalf("semantic node comparator accepted %s drift", test.name)
			}
		})
	}
}

func TestInsertOrValidateCanonicalEdgeRejectsIDAndTripleCollisions(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name     string
		existing func(CanonicalGraphEdge) CanonicalGraphEdge
	}{
		{
			name: "same ID different triple",
			existing: func(edge CanonicalGraphEdge) CanonicalGraphEdge {
				edge.To = "canon-node:different"
				return edge
			},
		},
		{
			name: "different ID same triple",
			existing: func(edge CanonicalGraphEdge) CanonicalGraphEdge {
				edge.ID = "canon-edge:different"
				return edge
			},
		},
		{
			name: "same identity provenance drift",
			existing: func(edge CanonicalGraphEdge) CanonicalGraphEdge {
				edge.Provenance.Producer = "drift"
				return edge
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newMockSQLDB()
			expected := canonicalGraphEdgeFixture()
			existing := test.existing(expected)
			db.canonicalGraphEdges[existing.ID] = existing
			_, err := insertOrValidateCanonicalEdge(ctx, &mockSQLTx{db: db}, expected, true)
			assertKind(t, err, ErrorCanonicalAdmissionInvariant)
		})
	}
}

func TestMockSQLAdmissionRejectsSharedNodeProducerDrift(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := testManualInput("mock-admission-shared-producer-drift")
	first, err := ingestManualText(ctx, db, input, testFixture())
	if err != nil {
		t.Fatalf("first ingestManualText() error = %v", err)
	}
	input.RequestID += "-second"
	second, err := ingestManualText(ctx, db, input, testFixture())
	if err != nil {
		t.Fatalf("second ingestManualText() error = %v", err)
	}
	firstAdmission, err := admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: first.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("first admitPendingProposal() error = %v", err)
	}
	node := db.canonicalGraphNodes[firstAdmission.CanonicalRef]
	node.Provenance.Producer = "forged-producer"
	db.canonicalGraphNodes[node.ID] = node

	_, err = admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: second.ProposalOccurrenceID})
	assertKind(t, err, ErrorCanonicalAdmissionInvariant)
	if got := db.proposalOccurrences[second.ProposalOccurrenceID].AdmissionOutcome; got != admissionOutcomePending {
		t.Fatalf("second proposal outcome = %q, want pending", got)
	}
}

func TestMockSQLAdmissionReplayRejectsCanonicalBodyAndBindingDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*mockSQLDB, AdmissionResult)
	}{
		{
			name: "missing manifest",
			mutate: func(db *mockSQLDB, admission AdmissionResult) {
				delete(db.ordinaryManifests, admission.AdmissionDecisionID)
			},
		},
		{
			name: "missing edge binding",
			mutate: func(db *mockSQLDB, admission AdmissionResult) {
				delete(db.ordinaryEdgeBindings, admission.AdmissionDecisionID)
			},
		},
		{
			name: "extra attributed node",
			mutate: func(db *mockSQLDB, admission AdmissionResult) {
				node := db.canonicalGraphNodes[admission.CanonicalRef]
				node.ID = "canon-node:extra-attributed-row"
				db.canonicalGraphNodes[node.ID] = node
			},
		},
		{
			name: "extra attributed edge",
			mutate: func(db *mockSQLDB, admission AdmissionResult) {
				edge := db.canonicalGraphEdges[admission.CanonicalEdgeIDs[0]]
				edge.ID = "canon-edge:extra-attributed-row"
				db.canonicalGraphEdges[edge.ID] = edge
			},
		},
		{
			name: "claim body",
			mutate: func(db *mockSQLDB, admission AdmissionResult) {
				node := db.canonicalGraphNodes[admission.CanonicalRef]
				node.Payload.Claim = "forged"
				db.canonicalGraphNodes[node.ID] = node
			},
		},
		{
			name: "edge body",
			mutate: func(db *mockSQLDB, admission AdmissionResult) {
				edge := db.canonicalGraphEdges[admission.CanonicalEdgeIDs[0]]
				edge.Provenance.TraceRef = "forged"
				db.canonicalGraphEdges[edge.ID] = edge
			},
		},
		{
			name: "node binding order",
			mutate: func(db *mockSQLDB, admission AdmissionResult) {
				bindings := db.ordinaryNodeBindings[admission.AdmissionDecisionID]
				bindings[1].Position = 7
				db.ordinaryNodeBindings[admission.AdmissionDecisionID] = bindings
			},
		},
		{
			name: "edge materialization",
			mutate: func(db *mockSQLDB, admission AdmissionResult) {
				bindings := db.ordinaryEdgeBindings[admission.AdmissionDecisionID]
				bindings[0].Materialization = canonicalRowReused
				db.ordinaryEdgeBindings[admission.AdmissionDecisionID] = bindings
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db := newMockSQLDB()
			proposal, err := ingestManualText(ctx, db, testManualInput("mock-admission-replay-drift-"+test.name), testFixture())
			if err != nil {
				t.Fatalf("ingestManualText() error = %v", err)
			}
			input := AdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID}
			admission, err := admitPendingProposal(ctx, db, input)
			if err != nil {
				t.Fatalf("first admitPendingProposal() error = %v", err)
			}
			test.mutate(db, admission)
			_, err = admitPendingProposal(ctx, db, input)
			assertKind(t, err, ErrorAdmissionReplayConflict)
		})
	}
}

func TestOrdinaryAdmissionMutationRejectsRepeatedIdentity(t *testing.T) {
	node := canonicalGraphNodeFixture(t)
	edge := canonicalGraphEdgeFixture()
	otherEdgeID := edge
	otherEdgeID.ID = "canon-edge:different-id-same-triple"
	for _, tc := range []struct {
		name     string
		mutation canonicalAdmissionMutation
	}{
		{name: "node ID", mutation: canonicalAdmissionMutation{nodes: []CanonicalGraphNode{node, node}}},
		{name: "edge ID", mutation: canonicalAdmissionMutation{edges: []CanonicalGraphEdge{edge, edge}}},
		{name: "edge triple", mutation: canonicalAdmissionMutation{edges: []CanonicalGraphEdge{edge, otherEdgeID}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertKind(t, validateCanonicalAdmissionMutationIdentity(tc.mutation), ErrorCanonicalAdmissionInvariant)
		})
	}
}

func canonicalGraphNodeFixture(t *testing.T) CanonicalGraphNode {
	t.Helper()
	proposal := ProposalQueryResult{
		ProposalOccurrenceID: "occ:expected",
		ProposalFingerprint:  "fingerprint",
		StatementText:        "expected claim",
		SourceBindingKind:    ProposalSourceBindingSourceSnapshot,
		SourceSnapshotID:     "srcsnap:expected",
		ExtractionViewID:     "view:expected",
		SourceSystem:         SourceSystemManualText,
		SourceID:             "expected",
		SourceVersion:        "v1",
		ExtractorName:        "extractor",
		SourceRefs: []ResolvedSourceRef{{
			ExtractionViewID: "view:expected",
			SpanID:           "span:1",
			StartByte:        0,
			EndByte:          8,
			QuotedTextHash:   "hash",
			QuotedText:       "expected",
		}},
	}
	node, err := buildClaimNode(proposal)
	if err != nil {
		t.Fatalf("buildClaimNode() error = %v", err)
	}
	return node
}

func canonicalGraphEdgeFixture() CanonicalGraphEdge {
	return CanonicalGraphEdge{
		ID:       "canon-edge:expected",
		From:     "canon-node:from",
		To:       "canon-node:to",
		Relation: evidencegraph.CanonicalSupportsClaim,
		Provenance: evidencegraph.ProvenanceRecord{
			ID:            "provenance:expected",
			OriginRefs:    []string{"source:expected"},
			OriginGroupID: "source:expected",
			Producer:      "extractor",
			Method:        "proposal_admission_edge",
			MethodVersion: "v1",
			TraceRef:      "fingerprint",
		},
		OriginProposalOccurrenceID: "occ:expected",
	}
}

func TestMockSQLAdmitPendingProposalReplayRejectsChangedDerivation(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	parent := admitCanonicalReadFixture(t, ctx, db, "mock-derived-replay-parent")
	proposal, err := ingestManualText(ctx, db, testManualInput("mock-derived-replay-proposal"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	input := AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		Derivation: &DerivationAdmissionInput{
			ParentNodeIDs: []string{parent.CanonicalRef},
			Method:        "policy_summary",
			Producer:      "unit-test",
			TraceRef:      "trace:derived-replay",
		},
	}
	if _, err := admitPendingProposal(ctx, db, input); err != nil {
		t.Fatalf("first admitPendingProposal() error = %v", err)
	}

	changed := input
	changed.Derivation = &DerivationAdmissionInput{
		ParentNodeIDs: []string{parent.CanonicalRef},
		Method:        "policy_rewrite",
		Producer:      "unit-test",
		TraceRef:      "trace:derived-replay",
	}
	_, err = admitPendingProposal(ctx, db, changed)
	assertKind(t, err, ErrorAdmissionReplayConflict)
	missingDerivation := normalizeAdmissionInput(input)
	missingDerivation.Derivation = nil
	_, err = admitPendingProposal(ctx, db, missingDerivation)
	assertKind(t, err, ErrorAdmissionReplayConflict)
}

func TestMockSQLDerivedAdmissionReplayAcceptsNormalizedEquivalentInput(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	parentA := admitCanonicalReadFixture(t, ctx, db, "mock-derived-normalized-parent-a")
	parentB := admitCanonicalReadFixture(t, ctx, db, "mock-derived-normalized-parent-b")
	proposal, err := ingestManualText(ctx, db, testManualInput("mock-derived-normalized-proposal"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	firstInput := AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		DecisionBy:           " reviewer-a ",
		DecisionReason:       " verified derivation ",
		Derivation: &DerivationAdmissionInput{
			ParentNodeIDs: []string{parentB.CanonicalRef, parentA.CanonicalRef},
			Method:        " policy_summary ",
			Producer:      " unit-test ",
			TraceRef:      " trace:normalized-replay ",
		},
	}
	first, err := admitPendingProposal(ctx, db, firstInput)
	if err != nil {
		t.Fatalf("first admitPendingProposal() error = %v", err)
	}
	replayInput := AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		DecisionBy:           "reviewer-a",
		DecisionReason:       "verified derivation",
		Derivation: &DerivationAdmissionInput{
			ParentNodeIDs: []string{parentA.CanonicalRef, parentB.CanonicalRef},
			Method:        "policy_summary",
			Producer:      "unit-test",
			TraceRef:      "trace:normalized-replay",
		},
	}
	replay, err := admitPendingProposal(ctx, db, replayInput)
	if err != nil {
		t.Fatalf("normalized replay admitPendingProposal() error = %v", err)
	}
	if !replay.Replayed || replay.DerivationID != first.DerivationID || !slices.Equal(replay.ParentNodeIDs, first.ParentNodeIDs) {
		t.Fatalf("normalized replay = %+v, want replay of %+v", replay, first)
	}

	changedProducer := replayInput
	changedProducer.Derivation = &DerivationAdmissionInput{
		ParentNodeIDs: append([]string(nil), replayInput.Derivation.ParentNodeIDs...),
		Method:        replayInput.Derivation.Method,
		Producer:      "different-producer",
		TraceRef:      replayInput.Derivation.TraceRef,
	}
	_, err = admitPendingProposal(ctx, db, changedProducer)
	assertKind(t, err, ErrorAdmissionReplayConflict)

	changedParents := replayInput
	changedParents.Derivation = &DerivationAdmissionInput{
		ParentNodeIDs: []string{parentA.CanonicalRef},
		Method:        replayInput.Derivation.Method,
		Producer:      replayInput.Derivation.Producer,
		TraceRef:      replayInput.Derivation.TraceRef,
	}
	_, err = admitPendingProposal(ctx, db, changedParents)
	assertKind(t, err, ErrorAdmissionReplayConflict)
}

func TestMockSQLAdmitPendingProposalReplayRejectsPersistedDerivationDrift(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	parent := admitCanonicalReadFixture(t, ctx, db, "mock-derived-persisted-drift-parent")
	proposal, err := ingestManualText(ctx, db, testManualInput("mock-derived-persisted-drift-proposal"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	input := AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		DecisionBy:           "reviewer-a",
		DecisionReason:       "verified derivation",
		Derivation: &DerivationAdmissionInput{
			ParentNodeIDs: []string{parent.CanonicalRef},
			Method:        "policy_summary",
			Producer:      "unit-test",
			TraceRef:      "trace:derived-persisted-drift",
		},
	}
	result, err := admitPendingProposal(ctx, db, input)
	if err != nil {
		t.Fatalf("first admitPendingProposal() error = %v", err)
	}
	original := db.canonicalDerivations[result.DerivationID]

	for _, test := range []struct {
		name   string
		mutate func(*evidencegraph.DerivationRecord)
	}{
		{name: "method", mutate: func(record *evidencegraph.DerivationRecord) { record.Method = "rewritten_method" }},
		{name: "producer", mutate: func(record *evidencegraph.DerivationRecord) { record.Producer = "rewritten-producer" }},
		{name: "trace ref", mutate: func(record *evidencegraph.DerivationRecord) { record.TraceRef = "trace:rewritten" }},
		{name: "provenance ref", mutate: func(record *evidencegraph.DerivationRecord) { record.ProvenanceRef = "provenance:rewritten" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			tampered := original
			test.mutate(&tampered)
			db.canonicalDerivations[result.DerivationID] = tampered
			defer func() { db.canonicalDerivations[result.DerivationID] = original }()

			_, err := admitPendingProposal(ctx, db, input)
			assertKind(t, err, ErrorAdmissionReplayConflict)
		})
	}

	parentID := original.Parents[0]
	originalEdgeID := db.derivationParentEdges[result.DerivationID][parentID]
	db.derivationParentEdges[result.DerivationID][parentID] = "canon-edge:rewritten-binding"
	_, err = admitPendingProposal(ctx, db, input)
	assertKind(t, err, ErrorAdmissionReplayConflict)
	db.derivationParentEdges[result.DerivationID][parentID] = originalEdgeID
}

func TestMockSQLSourceAdmissionReplayRejectsRogueDerivation(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	proposal, err := ingestManualText(ctx, db, testManualInput("mock-source-rogue-derivation"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	input := AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		DecisionBy:           "reviewer-a",
		DecisionReason:       "verified source claim",
	}
	result, err := admitPendingProposal(ctx, db, input)
	if err != nil {
		t.Fatalf("first admitPendingProposal() error = %v", err)
	}
	rogueID := "derivation:rogue"
	db.canonicalDerivations[rogueID] = evidencegraph.DerivationRecord{
		ID:            rogueID,
		NodeID:        result.CanonicalRef,
		Parents:       append([]string(nil), result.RawEvidenceNodeIDs...),
		Method:        "rogue",
		Producer:      "rogue",
		TraceRef:      "trace:rogue",
		ProvenanceRef: "provenance:rogue",
	}
	db.derivationOrigins[rogueID] = proposal.ProposalOccurrenceID
	db.derivationParentEdges[rogueID] = map[string]string{
		result.RawEvidenceNodeIDs[0]: result.CanonicalEdgeIDs[0],
	}

	_, err = admitPendingProposal(ctx, db, input)
	assertKind(t, err, ErrorAdmissionReplayConflict)
}

func TestMockSQLAdmissionReplayPreservesPersistedIntegrityError(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	proposal, err := ingestManualText(ctx, db, testManualInput("mock-source-replay-integrity"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	input := normalizeAdmissionInput(AdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID})
	if _, err := admitPendingProposal(ctx, db, input); err != nil {
		t.Fatalf("first admitPendingProposal() error = %v", err)
	}
	tx := &mockSQLTx{db: db}
	loaded, err := loadProposalForAdmission(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("loadProposalForAdmission() error = %v", err)
	}
	replay, metadata, err := loadAdmissionDecisionResult(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("loadAdmissionDecisionResult() error = %v", err)
	}
	// This field is persisted provenance reconstructed by the loader, not caller
	// replay input. Its corruption must retain the integrity classification.
	loaded.SourceBindingKind = "corrupt-binding"
	err = validateAdmissionReplay(loaded, input, replay, metadata)
	assertKind(t, err, ErrorRepositorySnapshotIntegrity)
}

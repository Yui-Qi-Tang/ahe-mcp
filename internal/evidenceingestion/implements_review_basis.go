package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/jackc/pgx/v5"
)

const (
	derivedImplementsMaxParents  = 8
	derivedImplementsSourceBytes = 1 << 20
	derivedImplementsRecordBytes = 128 << 10
)

// DerivedImplementsSourceLeaf preserves an admitted node and its reconstructed
// original submission basis. Reconstruction does not prove prior rendering.
type DerivedImplementsSourceLeaf struct {
	Node           CanonicalQueryResult
	ReviewSnapshot ReviewableSourceClaimReviewSnapshot
	RawText        string
	RenderedText   string
}

// DerivedImplementsNativeBasis is an independently loaded AND cut.
// It does not assert semantic truth, source freshness, or relation permission.
type DerivedImplementsNativeBasis struct {
	Specification  CanonicalQueryResult
	Implementation CanonicalQueryResult
	Ancestors      evidencegraph.CanonicalArtifact
	SourceLeaves   []DerivedImplementsSourceLeaf
	// DerivedNodes includes the root and every derived ancestor for recursive
	// review. The historical depth-one loader leaves this field unset.
	DerivedNodes []CanonicalQueryResult
}

// LoadDerivedImplementsNativeBasisInTx loads exact native authority in its
// caller's repeatable-read (or serializable) transaction. Only node coordinates
// cross this boundary; no submitted source maps or graph bodies are trusted.
func LoadDerivedImplementsNativeBasisInTx(ctx context.Context, tx pgx.Tx, specificationID, implementationID string) (DerivedImplementsNativeBasis, error) {
	if tx == nil {
		return DerivedImplementsNativeBasis{}, newDomainError(ErrorInvalidInput, "postgres transaction is required")
	}
	return loadDerivedImplementsNativeBasis(ctx, pgxTx{tx: tx}, specificationID, implementationID)
}

func loadDerivedImplementsNativeBasis(ctx context.Context, tx sqlTx, specificationID, implementationID string) (DerivedImplementsNativeBasis, error) {
	if err := validateDerivedImplementsCoordinates(specificationID, implementationID); err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	var isolation string
	if err := tx.queryRow(ctx, `SELECT current_setting('transaction_isolation')`).Scan(&isolation); err != nil {
		return DerivedImplementsNativeBasis{}, fmt.Errorf("reading implements review isolation: %w", err)
	}
	if isolation != "repeatable read" && isolation != "serializable" {
		return DerivedImplementsNativeBasis{}, newDomainError(ErrorInvalidInput, "implements review requires repeatable-read or serializable isolation")
	}
	root, metadata, err := loadDerivedImplementsAdmittedNode(ctx, tx, specificationID, evidencegraph.CanonicalDerivedClaim)
	if err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	if metadata.Derivation == nil || len(metadata.Derivation.Parents) < 1 || len(metadata.Derivation.Parents) > derivedImplementsMaxParents {
		return DerivedImplementsNativeBasis{}, newDomainError(ErrorDerivationInvariant, "implements specification requires one to eight direct source parents")
	}
	result := DerivedImplementsNativeBasis{Specification: root, SourceLeaves: make([]DerivedImplementsSourceLeaf, 0, len(metadata.Derivation.Parents))}
	edges := make(map[string]canonicalReadEdge, len(metadata.Derivation.Parents))
	nodeIDs := make([]string, 0, len(metadata.Derivation.Parents)+1)
	nodeIDs = append(nodeIDs, root.CanonicalID)
	for _, parentID := range metadata.Derivation.Parents {
		parent, _, err := loadDerivedImplementsAdmittedNode(ctx, tx, parentID, evidencegraph.CanonicalSourceClaim)
		if err != nil {
			return DerivedImplementsNativeBasis{}, err
		}
		leaf, err := loadDerivedImplementsSourceLeaf(ctx, tx, parent)
		if err != nil {
			return DerivedImplementsNativeBasis{}, err
		}
		result.SourceLeaves = append(result.SourceLeaves, leaf)
		nodeIDs = append(nodeIDs, parentID)
		edgeID := metadata.DerivationParentEdges[parentID]
		var edge canonicalReadEdge
		var relation, owner string
		var provenance []byte
		if err := tx.queryRow(ctx, `SELECT canonical_edge_id,from_node_id,to_node_id,relation,provenance,origin_proposal_occurrence_id
			FROM canonical_graph_edges WHERE canonical_edge_id=$1`, edgeID).Scan(&edge.id, &edge.from, &edge.to, &relation, &provenance, &owner); err != nil {
			return DerivedImplementsNativeBasis{}, fmt.Errorf("loading exact implements derivation edge: %w", err)
		}
		if edge.from != parentID || edge.to != root.CanonicalID || relation != string(evidencegraph.CanonicalDerivedFrom) || owner != root.OriginProposalOccurrenceID {
			return DerivedImplementsNativeBasis{}, newDomainError(ErrorDerivationInvariant, "implements derivation parent edge or ownership differs")
		}
		edge.relation = evidencegraph.CanonicalDerivedFrom
		if err := json.Unmarshal(provenance, &edge.provenance); err != nil {
			return DerivedImplementsNativeBasis{}, fmt.Errorf("decoding implements derivation edge: %w", err)
		}
		edges[edgeID] = edge
	}
	slices.Sort(nodeIDs)
	result.Ancestors, err = assembleCanonicalReadArtifact(ctx, tx, nodeIDs, edges, []string{root.CanonicalID})
	if err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	result.Implementation, _, err = loadDerivedImplementsAdmittedNode(ctx, tx, implementationID, evidencegraph.CanonicalSourceClaim)
	if err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	if err := validateDerivedImplementsCode(ctx, tx, result.Implementation.OriginProposal); err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	return result, nil
}

func validateDerivedImplementsCoordinates(specificationID, implementationID string) error {
	for _, id := range []string{specificationID, implementationID} {
		if !strings.HasPrefix(id, "canon-node:") || len(id) > 512 || strings.TrimSpace(id) != id || !utf8.ValidString(id) || strings.ContainsRune(id, '\x00') {
			return newDomainError(ErrorInvalidRecordID, "implements endpoint must be a bounded canonical node ID")
		}
	}
	if specificationID == implementationID {
		return newDomainError(ErrorInvalidInput, "implements endpoints must differ")
	}
	return nil
}

func loadDerivedImplementsAdmittedNode(ctx context.Context, tx sqlTx, nodeID string, kind evidencegraph.CanonicalNodeKind) (CanonicalQueryResult, admissionDecisionMetadata, error) {
	fail := func(err error) (CanonicalQueryResult, admissionDecisionMetadata, error) {
		return CanonicalQueryResult{}, admissionDecisionMetadata{}, err
	}
	// Bound native JSON and the exact parent count before legacy loaders allocate.
	var size, refs, parentCount, incomingParents int
	err := tx.queryRow(ctx, `SELECT octet_length(n.payload::text)+octet_length(n.provenance::text)+octet_length(n.temporal::text)+octet_length(n.integrity::text)
		+octet_length(p.source_refs::text)+octet_length(p.proposed_payload::text), jsonb_array_length(p.source_refs),
		(SELECT count(*) FROM canonical_derivation_parents dp JOIN canonical_derivations d USING(derivation_id) WHERE d.node_id=n.canonical_node_id),
		(SELECT count(*) FROM canonical_graph_edges e WHERE e.to_node_id=n.canonical_node_id AND e.relation='derived_from')
		FROM canonical_graph_nodes n JOIN proposal_occurrences p ON p.proposal_occurrence_id=n.origin_proposal_occurrence_id WHERE n.canonical_node_id=$1`, nodeID).Scan(&size, &refs, &parentCount, &incomingParents)
	if err != nil {
		return fail(fmt.Errorf("preflighting implements canonical node: %w", err))
	}
	if size > derivedImplementsRecordBytes || refs > 64 || parentCount > derivedImplementsMaxParents || incomingParents != parentCount {
		return fail(newDomainError(ErrorInvalidInput, "implements native node exceeds bounded review limits"))
	}
	node, err := loadCanonicalNode(ctx, tx, nodeID)
	if err != nil {
		return fail(err)
	}
	if node.NodeKind != kind {
		return fail(newDomainError(ErrorUnsupportedAdmission, "implements endpoint %s requires kind %s; deeper derivations are unsupported", nodeID, kind))
	}
	if err := validateCanonicalNodeFirstMaterializerAuthority(ctx, tx, nodeID, node.OriginProposalOccurrenceID); err != nil {
		return fail(err)
	}
	proposal, err := traceProposalProvenance(ctx, tx, node.OriginProposalOccurrenceID)
	if err != nil {
		return fail(err)
	}
	if proposal.AdmissionOutcome != admissionOutcomeAdmitted || proposal.CanonicalRef != nodeID || proposal.ExtractionAttemptStatus != attemptStatusSucceeded {
		return fail(newDomainError(ErrorCanonicalAdmissionInvariant, "implements endpoint differs from its admitted origin"))
	}
	// Classify the first materializer by its own proposal event, not lineage
	// membership: an ordinary source may later become a bootstrapped target.
	supersession, err := proposalHasSupersessionAdmissionEvent(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return fail(err)
	}
	if supersession {
		metadata, err := replayImplementsSupersessionOrigin(ctx, tx, proposal)
		if err != nil {
			return fail(err)
		}
		node.OriginProposal = proposal
		return node, metadata, nil
	}
	replay, metadata, err := loadAdmissionDecisionResult(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return fail(err)
	}
	input := AdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID, DecisionBy: metadata.DecisionBy, DecisionReason: metadata.DecisionReason}
	if metadata.Derivation != nil {
		d := metadata.Derivation
		input.Derivation = &DerivationAdmissionInput{ParentNodeIDs: d.Parents, Method: d.Method, Producer: d.Producer, TraceRef: d.TraceRef}
	}
	if err := validateAdmissionReplay(proposal, input, replay, metadata); err != nil {
		return fail(err)
	}
	if metadata.ReviewBindingContractVersion == ReviewedEndpointAdmissionV1 {
		if err := validateEndpointBinding(ctx, tx, proposal, input); err != nil {
			return fail(err)
		}
	}
	// Ordinary/reviewed source and derived origins keep their exact original
	// manifest proof; a failed origin never falls back to another authority.
	if err := validatePersistedOrdinaryAdmissionMutation(ctx, tx, proposal, input); err != nil {
		return fail(err)
	}
	node.OriginProposal = proposal
	return node, metadata, nil
}

func loadDerivedImplementsSourceLeaf(ctx context.Context, tx sqlTx, node CanonicalQueryResult) (DerivedImplementsSourceLeaf, error) {
	p := node.OriginProposal
	if p.SourceBindingKind != ProposalSourceBindingSourceSnapshot || (p.SourceSystem != SourceSystemExternalDocument && p.SourceSystem != SourceSystemManualText) {
		return DerivedImplementsSourceLeaf{}, newDomainError(ErrorUnsupportedAdmission, "implements source parents require original qualified source review metadata")
	}
	var rawSize, renderedSize, outputSize, occurrenceCount, spanCount int
	err := tx.queryRow(ctx, `SELECT octet_length(b.raw_content),octet_length(v.rendered_content),octet_length(a.fixture_output::text),
		(SELECT count(*) FROM proposal_occurrences WHERE extraction_attempt_id=a.extraction_attempt_id),
		(SELECT count(*) FROM span_catalog_entries WHERE extraction_view_id=v.extraction_view_id)
		FROM extraction_attempts a JOIN extraction_runs r USING(extraction_run_id)
		JOIN source_snapshots s ON s.source_snapshot_id=r.source_snapshot_id JOIN source_blobs b USING(raw_content_hash)
		JOIN extraction_views v ON v.extraction_view_id=r.extraction_view_id AND v.source_snapshot_id=s.source_snapshot_id
		WHERE a.extraction_attempt_id=$1`, p.ExtractionAttemptID).Scan(&rawSize, &renderedSize, &outputSize, &occurrenceCount, &spanCount)
	if err != nil {
		return DerivedImplementsSourceLeaf{}, fmt.Errorf("preflighting implements source reconstruction: %w", err)
	}
	if rawSize > derivedImplementsSourceBytes || renderedSize > derivedImplementsSourceBytes || outputSize > derivedImplementsSourceBytes || occurrenceCount > 205 || spanCount > 4096 {
		return DerivedImplementsSourceLeaf{}, newDomainError(ErrorInvalidInput, "implements source reconstruction exceeds bounded review limits")
	}
	snapshot, err := loadSourceClaimReviewSnapshotForLifecycleInTx(ctx, tx, p.ExtractionAttemptID, p.ProposalOccurrenceID, node.CanonicalID)
	if err != nil {
		return DerivedImplementsSourceLeaf{}, err
	}
	var raw, rendered []byte
	err = tx.queryRow(ctx, `SELECT b.raw_content,v.rendered_content FROM source_snapshots s JOIN source_blobs b USING(raw_content_hash)
		JOIN extraction_views v ON v.source_snapshot_id=s.source_snapshot_id WHERE s.source_snapshot_id=$1 AND v.extraction_view_id=$2`, p.SourceSnapshotID, p.ExtractionViewID).Scan(&raw, &rendered)
	if err != nil {
		return DerivedImplementsSourceLeaf{}, fmt.Errorf("loading implements original source bytes: %w", err)
	}
	basis := snapshot.ReviewPackage.ProposalBasis
	if p.SourceSystem == SourceSystemManualText && basis.ManualReviewProfile == nil {
		return DerivedImplementsSourceLeaf{}, newDomainError(ErrorUnsupportedAdmission, "implements manual source parents require a native first-capture review profile")
	}
	if contentHash(raw) != basis.RawContentHash || contentHash(rendered) != basis.RenderedContentHash || !utf8.Valid(raw) || !utf8.Valid(rendered) ||
		basis.StatementText != node.Payload.Claim || basis.SourceTitle == "" || basis.SourceLocation == "" {
		return DerivedImplementsSourceLeaf{}, newDomainError(ErrorReviewContractConflict, "implements original source bytes, statement or metadata differ")
	}
	return DerivedImplementsSourceLeaf{Node: node, ReviewSnapshot: snapshot, RawText: string(raw), RenderedText: string(rendered)}, nil
}

func validateDerivedImplementsCode(ctx context.Context, tx sqlTx, p ProposalQueryResult) error {
	if p.SourceBindingKind != ProposalSourceBindingRepositorySnapshot || p.RepositorySnapshot == nil || p.CodeFact == nil || p.CodeRelation != nil || len(p.SourceRefs) != 1 {
		return newDomainError(ErrorUnsupportedAdmission, "implements code requires one repository-backed source reference and a verified code fact")
	}
	ref := p.SourceRefs[0]
	var file RepositoryExtractorFile
	if err := tx.queryRow(ctx, `SELECT f.file_snapshot_id,f.repository_snapshot_id,f.repo_id,f.commit_sha,f.path,f.blob_hash,f.git_blob_oid,f.byte_length,b.raw_content
		FROM source_file_snapshots f JOIN source_blobs b ON b.raw_content_hash=f.blob_hash
		WHERE f.file_snapshot_id=$1 AND f.repository_snapshot_id=$2 AND octet_length(b.raw_content)<=$3`, ref.FileSnapshotID, p.RepositorySnapshot.ID, derivedImplementsSourceBytes).
		Scan(&file.FileSnapshot.ID, &file.FileSnapshot.RepositorySnapshotID, &file.FileSnapshot.RepoID, &file.FileSnapshot.CommitSHA, &file.FileSnapshot.Path,
			&file.FileSnapshot.BlobHash, &file.FileSnapshot.GitBlobOID, &file.FileSnapshot.ByteLength, &file.Content); err != nil {
		return fmt.Errorf("loading implements immutable code file: %w", err)
	}
	return validateDerivedImplementsCodeFile(p, file)
}

func validateDerivedImplementsCodeFile(p ProposalQueryResult, file RepositoryExtractorFile) error {
	if p.RepositorySnapshot == nil || p.CodeFact == nil || len(p.SourceRefs) != 1 {
		return newDomainError(ErrorUnsupportedAdmission, "implements code fact and exact source reference are required")
	}
	if err := validateRepositoryExtractorFile(*p.RepositorySnapshot, file); err != nil {
		return err
	}
	ref, fact := p.SourceRefs[0], *p.CodeFact
	if !utf8.Valid(file.Content) || ref.TargetKind != "file_snapshot" || ref.ExtractionViewID != "" || ref.RepositorySnapshotID != p.RepositorySnapshot.ID || ref.FileSnapshotID != file.FileSnapshot.ID ||
		ref.RepoID != file.FileSnapshot.RepoID || ref.CommitSHA != file.FileSnapshot.CommitSHA || ref.Path != file.FileSnapshot.Path ||
		ref.StartByte < 0 || ref.EndByte <= ref.StartByte || ref.EndByte > len(file.Content) ||
		string(file.Content[ref.StartByte:ref.EndByte]) != ref.QuotedText || contentHash([]byte(ref.QuotedText)) != ref.QuotedTextHash ||
		fact.StartByte < ref.StartByte || fact.EndByte > ref.EndByte || fact.EndByte <= fact.StartByte ||
		string(file.Content[fact.StartByte:fact.EndByte]) != fact.QuotedText || contentHash([]byte(fact.QuotedText)) != fact.QuotedTextHash {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "implements code fact differs from immutable file bytes")
	}
	index, err := goCodeFactIndex(repositoryFileAttemptContext(*p.RepositorySnapshot, file))
	if err != nil {
		return err
	}
	if err := verifyGoCodeFact(index, fact); err != nil {
		return err
	}
	if fact.StartLine != codeLineForOffset(file.Content, fact.StartByte) || fact.EndLine != codeLineForOffset(file.Content, fact.EndByte-1) {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "implements code fact line coordinates differ")
	}
	parsed := index[codeFactSpan{startByte: fact.StartByte, endByte: fact.EndByte}]
	if p.StatementText != codeFactStatement(parsed, fact.StartLine, fact.EndLine) {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "implements code statement differs from the parsed declaration")
	}
	fingerprint, err := repositoryCodeFactProposalFingerprint(p.StatementText, p.SourceRefs, fact)
	if err != nil {
		return err
	}
	if fingerprint != p.ProposalFingerprint || p.ProposalFingerprintVersion != ProposalFingerprintCodeFactV1 {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "implements code proposal fingerprint differs")
	}
	return nil
}

package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type repositoryProposalIdentity struct {
	OccurrenceID string
	Identity     string
}

type repositoryProposalReconciliationItem struct {
	State                string
	ProposalIdentity     string
	CurrentOccurrenceID  string
	PreviousOccurrenceID string
}

func reconcileRepositorySourceGeneration(
	ctx context.Context,
	tx sqlTx,
	target RepositorySourceGeneration,
	previousGenerationID string,
) (RepositoryGenerationReconciliation, error) {
	current, err := loadRepositoryProposalIdentities(ctx, tx, target)
	if err != nil {
		return RepositoryGenerationReconciliation{}, err
	}

	var previous []repositoryProposalIdentity
	if previousGenerationID != "" {
		previousGeneration, err := readRepositorySourceGenerationByID(ctx, tx, previousGenerationID)
		if err != nil {
			return RepositoryGenerationReconciliation{}, fmt.Errorf("reading previous repository source generation: %w", err)
		}
		if previousGeneration.RepoID != target.RepoID || previousGeneration.ExtractorName != target.ExtractorName || previousGeneration.Number >= target.Number {
			return RepositoryGenerationReconciliation{}, newDomainError(ErrorSourceGenerationConflict, "repository source generation %s cannot reconcile against generation %s outside its prior stream", target.ID, previousGeneration.ID)
		}
		previous, err = loadRepositoryProposalIdentities(ctx, tx, previousGeneration)
		if err != nil {
			return RepositoryGenerationReconciliation{}, err
		}
	}

	items := classifyRepositoryProposalIdentities(current, previous)
	reconciliation := RepositoryGenerationReconciliation{
		SourceGenerationID:   target.ID,
		PreviousGenerationID: previousGenerationID,
		IdentityContract:     RepositoryProposalIdentityContractV1,
	}
	for _, item := range items {
		switch item.State {
		case RepositoryProposalLifecycleNew:
			reconciliation.NewCount++
		case RepositoryProposalLifecycleUnchanged:
			reconciliation.UnchangedCount++
		case RepositoryProposalLifecycleStale:
			reconciliation.StaleCount++
		default:
			return RepositoryGenerationReconciliation{}, newDomainError(ErrorSourceGenerationConflict, "repository source generation %s produced unsupported reconciliation state %q", target.ID, item.State)
		}
	}
	if err := insertRepositoryGenerationReconciliation(ctx, tx, target, reconciliation, items); err != nil {
		return RepositoryGenerationReconciliation{}, err
	}
	return reconciliation, nil
}

func loadRepositoryProposalIdentities(ctx context.Context, tx sqlTx, generation RepositorySourceGeneration) ([]repositoryProposalIdentity, error) {
	rows, err := tx.query(ctx, `
		SELECT
			po.proposal_occurrence_id,
			po.proposal_fingerprint,
			po.proposal_fingerprint_version,
			po.proposal_kind,
			po.source_refs
		FROM proposal_occurrences po
		WHERE po.proposal_batch_id = $1
		ORDER BY po.proposal_occurrence_id
	`, generation.ProposalBatchID)
	if err != nil {
		return nil, fmt.Errorf("querying repository generation proposal identities: %w", err)
	}
	defer rows.Close()

	identities := make([]repositoryProposalIdentity, 0, generation.ProposalCount)
	for rows.Next() {
		var occurrenceID, fingerprint, fingerprintVersion, proposalKind string
		var sourceRefsData []byte
		if err := rows.Scan(&occurrenceID, &fingerprint, &fingerprintVersion, &proposalKind, &sourceRefsData); err != nil {
			return nil, fmt.Errorf("scanning repository generation proposal identity: %w", err)
		}
		var sourceRefs []ResolvedSourceRef
		if err := json.Unmarshal(sourceRefsData, &sourceRefs); err != nil {
			return nil, fmt.Errorf("decoding proposal %s source refs for reconciliation: %w", occurrenceID, err)
		}
		if err := validateRepositoryProposalIdentitySourceRefs(generation, occurrenceID, sourceRefs); err != nil {
			return nil, err
		}
		data, err := deterministicJSON(struct {
			ProposalFingerprint        string              `json:"proposal_fingerprint"`
			ProposalFingerprintVersion string              `json:"proposal_fingerprint_version"`
			ProposalKind               string              `json:"proposal_kind"`
			SourceRefs                 []ResolvedSourceRef `json:"source_refs"`
		}{fingerprint, fingerprintVersion, proposalKind, sourceRefs})
		if err != nil {
			return nil, fmt.Errorf("serializing proposal %s reconciliation identity: %w", occurrenceID, err)
		}
		identities = append(identities, repositoryProposalIdentity{
			OccurrenceID: occurrenceID,
			Identity:     contentHash(data),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating repository generation proposal identities: %w", err)
	}
	if len(identities) != generation.ProposalCount {
		return nil, newDomainError(ErrorSourceGenerationConflict, "repository source generation %s declares %d proposals but binds %d occurrences", generation.ID, generation.ProposalCount, len(identities))
	}
	return identities, nil
}

func validateRepositoryProposalIdentitySourceRefs(generation RepositorySourceGeneration, occurrenceID string, refs []ResolvedSourceRef) error {
	if len(refs) == 0 {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository proposal %s has no source references for reconciliation", occurrenceID)
	}
	for _, ref := range refs {
		if ref.RepositorySnapshotID != generation.RepositorySnapshotID || ref.RepoID != generation.RepoID || ref.CommitSHA != generation.CommitSHA || ref.FileSnapshotID == "" {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository proposal %s source reference does not match generation %s", occurrenceID, generation.ID)
		}
	}
	return nil
}

func classifyRepositoryProposalIdentities(current, previous []repositoryProposalIdentity) []repositoryProposalReconciliationItem {
	currentByIdentity := groupRepositoryProposalIdentities(current)
	previousByIdentity := groupRepositoryProposalIdentities(previous)
	keys := make([]string, 0, len(currentByIdentity)+len(previousByIdentity))
	seen := make(map[string]struct{}, len(currentByIdentity)+len(previousByIdentity))
	for identity := range currentByIdentity {
		seen[identity] = struct{}{}
		keys = append(keys, identity)
	}
	for identity := range previousByIdentity {
		if _, ok := seen[identity]; ok {
			continue
		}
		keys = append(keys, identity)
	}
	sort.Strings(keys)

	items := make([]repositoryProposalReconciliationItem, 0, len(current)+len(previous))
	for _, identity := range keys {
		currentOccurrences := currentByIdentity[identity]
		previousOccurrences := previousByIdentity[identity]
		matched := min(len(currentOccurrences), len(previousOccurrences))
		for i := 0; i < matched; i++ {
			items = append(items, repositoryProposalReconciliationItem{
				State:                RepositoryProposalLifecycleUnchanged,
				ProposalIdentity:     identity,
				CurrentOccurrenceID:  currentOccurrences[i],
				PreviousOccurrenceID: previousOccurrences[i],
			})
		}
		for _, occurrenceID := range currentOccurrences[matched:] {
			items = append(items, repositoryProposalReconciliationItem{
				State:               RepositoryProposalLifecycleNew,
				ProposalIdentity:    identity,
				CurrentOccurrenceID: occurrenceID,
			})
		}
		for _, occurrenceID := range previousOccurrences[matched:] {
			items = append(items, repositoryProposalReconciliationItem{
				State:                RepositoryProposalLifecycleStale,
				ProposalIdentity:     identity,
				PreviousOccurrenceID: occurrenceID,
			})
		}
	}
	return items
}

func groupRepositoryProposalIdentities(proposals []repositoryProposalIdentity) map[string][]string {
	grouped := make(map[string][]string)
	for _, proposal := range proposals {
		grouped[proposal.Identity] = append(grouped[proposal.Identity], proposal.OccurrenceID)
	}
	for identity := range grouped {
		sort.Strings(grouped[identity])
	}
	return grouped
}

func insertRepositoryGenerationReconciliation(
	ctx context.Context,
	tx sqlTx,
	target RepositorySourceGeneration,
	reconciliation RepositoryGenerationReconciliation,
	items []repositoryProposalReconciliationItem,
) error {
	var previous any
	if reconciliation.PreviousGenerationID != "" {
		previous = reconciliation.PreviousGenerationID
	}
	if _, err := tx.exec(ctx, `
		INSERT INTO repository_generation_reconciliations (
			source_generation_id,
			repo_id,
			extractor_name,
			previous_generation_id,
			identity_contract,
			unchanged_count,
			new_count,
			stale_count
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, reconciliation.SourceGenerationID, target.RepoID, target.ExtractorName, previous, reconciliation.IdentityContract, reconciliation.UnchangedCount, reconciliation.NewCount, reconciliation.StaleCount); err != nil {
		return fmt.Errorf("inserting repository generation reconciliation: %w", err)
	}

	for _, item := range items {
		itemID, err := stableID("reconciliation-item:", "repository_generation_proposal_reconciliation", struct {
			SourceGenerationID   string `json:"source_generation_id"`
			State                string `json:"state"`
			ProposalIdentity     string `json:"proposal_identity"`
			CurrentOccurrenceID  string `json:"current_proposal_occurrence_id,omitempty"`
			PreviousOccurrenceID string `json:"previous_proposal_occurrence_id,omitempty"`
		}{reconciliation.SourceGenerationID, item.State, item.ProposalIdentity, item.CurrentOccurrenceID, item.PreviousOccurrenceID})
		if err != nil {
			return err
		}
		var current, previous any
		if item.CurrentOccurrenceID != "" {
			current = item.CurrentOccurrenceID
		}
		if item.PreviousOccurrenceID != "" {
			previous = item.PreviousOccurrenceID
		}
		if _, err := tx.exec(ctx, `
			INSERT INTO repository_generation_proposal_reconciliations (
				reconciliation_item_id,
				source_generation_id,
				lifecycle_state,
				proposal_identity,
				current_proposal_occurrence_id,
				previous_proposal_occurrence_id
			)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, itemID, reconciliation.SourceGenerationID, item.State, item.ProposalIdentity, current, previous); err != nil {
			return fmt.Errorf("inserting repository proposal reconciliation item: %w", err)
		}
	}
	return nil
}

func readRepositoryGenerationReconciliation(ctx context.Context, tx sqlTx, sourceGenerationID string) (RepositoryGenerationReconciliation, error) {
	var reconciliation RepositoryGenerationReconciliation
	var previousGenerationID *string
	err := tx.queryRow(ctx, `
		SELECT
			source_generation_id,
			previous_generation_id,
			identity_contract,
			unchanged_count,
			new_count,
			stale_count
		FROM repository_generation_reconciliations
		WHERE source_generation_id = $1
	`, sourceGenerationID).Scan(
		&reconciliation.SourceGenerationID,
		&previousGenerationID,
		&reconciliation.IdentityContract,
		&reconciliation.UnchangedCount,
		&reconciliation.NewCount,
		&reconciliation.StaleCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryGenerationReconciliation{}, newDomainError(ErrorSourceGenerationConflict, "activated repository source generation %s has no reconciliation", sourceGenerationID)
	}
	if err != nil {
		return RepositoryGenerationReconciliation{}, fmt.Errorf("reading repository generation reconciliation: %w", err)
	}
	if previousGenerationID != nil {
		reconciliation.PreviousGenerationID = *previousGenerationID
	}
	if reconciliation.IdentityContract != RepositoryProposalIdentityContractV1 {
		return RepositoryGenerationReconciliation{}, newDomainError(ErrorSourceGenerationConflict, "repository source generation %s uses unsupported reconciliation identity contract %q", sourceGenerationID, reconciliation.IdentityContract)
	}
	return reconciliation, nil
}

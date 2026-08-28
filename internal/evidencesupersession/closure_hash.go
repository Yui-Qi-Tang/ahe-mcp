package evidencesupersession

import "fmt"

func closureHistoryHash(head ClosureHead, events []ClosureEventRecord) (string, error) {
	payload := struct {
		ContractVersion string               `json:"contract_version"`
		Head            ClosureHead          `json:"head"`
		Events          []ClosureEventRecord `json:"events"`
	}{
		ContractVersion: AdmissionEventContractVersionV2,
		Head:            head,
		Events:          events,
	}
	digest, err := hashJSON(payload)
	if err != nil {
		return "", fmt.Errorf("hashing supersession history: %w", err)
	}
	return "supersession-history:v2:" + digest, nil
}

func closureObjectManifestHash(basis Basis, claims []ObjectClaimClassification) (string, error) {
	type sourceObjectScope struct {
		SourceSystem    string `json:"source_system"`
		SourceNamespace string `json:"source_namespace"`
		ObjectType      string `json:"object_type"`
		ObjectID        string `json:"object_id"`
	}
	payload := struct {
		ContractVersion string                      `json:"contract_version"`
		CoveragePolicy  string                      `json:"coverage_policy"`
		SourceObject    sourceObjectScope           `json:"source_object"`
		Claims          []ObjectClaimClassification `json:"claims"`
	}{
		ContractVersion: ClosureCutContractVersionV1,
		CoveragePolicy:  ObjectCoveragePolicyV1,
		SourceObject: sourceObjectScope{
			SourceSystem:    basis.SourceSystem,
			SourceNamespace: basis.SourceNamespace,
			ObjectType:      basis.ObjectType,
			ObjectID:        basis.ObjectID,
		},
		Claims: claims,
	}
	digest, err := hashJSON(payload)
	if err != nil {
		return "", fmt.Errorf("hashing supersession object manifest: %w", err)
	}
	return "object-claim-manifest:v1:" + digest, nil
}

func closureCutHash(
	lineageKey string,
	basis Basis,
	head ClosureHead,
	historyHash string,
	members []ClosureMemberRecord,
	edges []ClosureEdgeRecord,
) (string, error) {
	payload := struct {
		ContractVersion string                `json:"contract_version"`
		LineageKey      string                `json:"lineage_key"`
		Basis           Basis                 `json:"basis"`
		Head            ClosureHead           `json:"head"`
		HistoryHash     string                `json:"history_hash"`
		Members         []ClosureMemberRecord `json:"members"`
		Edges           []ClosureEdgeRecord   `json:"edges"`
	}{
		ContractVersion: ClosureCutContractVersionV1,
		LineageKey:      lineageKey,
		Basis:           basis,
		Head:            head,
		HistoryHash:     historyHash,
		Members:         members,
		Edges:           edges,
	}
	digest, err := hashJSON(payload)
	if err != nil {
		return "", fmt.Errorf("hashing supersession closure cut: %w", err)
	}
	return "closure-cut:v1:" + digest, nil
}

func cloneClosureEvents(events []ClosureEventRecord) []ClosureEventRecord {
	cloned := make([]ClosureEventRecord, len(events))
	for index, event := range events {
		cloned[index] = event
		cloned[index].Payload.TargetNodeIDs = cloneClosureStrings(event.Payload.TargetNodeIDs)
		cloned[index].Payload.BootstrappedTargetNodeIDs = cloneClosureStrings(
			event.Payload.BootstrappedTargetNodeIDs,
		)
	}
	return cloned
}

func cloneClosureStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append(make([]string, 0, len(values)), values...)
}

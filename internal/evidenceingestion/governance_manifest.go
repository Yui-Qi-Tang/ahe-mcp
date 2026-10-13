package evidenceingestion

import (
	"encoding/json"
	"fmt"
	"reflect"
)

const GovernanceBatchV1 = "evidence-governance-batch/v1"

// GovernanceBatchRequest preserves the historical record independently of the
// reason proposed for this new native decision. Neither is a human approval.
type GovernanceBatchRequest struct {
	Record InterimGovernanceRecord `json:"record"`
	Reason string                  `json:"reason"`
}

// GovernanceBatchManifestItem binds one historical record and its exact native
// operation, subject and proposed reason. Non-migratable entries remain visible.
type GovernanceBatchManifestItem struct {
	Record          InterimGovernanceRecord `json:"record"`
	RecordHash      string                  `json:"record_hash"`
	Status          string                  `json:"status"`
	Error           string                  `json:"error,omitempty"`
	Reason          string                  `json:"reason"`
	NativeRequestID string                  `json:"native_request_id,omitempty"`
	NativeSubject   string                  `json:"native_subject,omitempty"`
	NativeRequest   *GovernanceRequest      `json:"native_request,omitempty"`
}

// GovernanceBatchManifest is the exact ordered review set, including blocked
// and verification-only records. Its digest binds the complete set and reasons.
type GovernanceBatchManifest struct {
	Contract string                        `json:"contract"`
	StoreID  string                        `json:"store_id"`
	Items    []GovernanceBatchManifestItem `json:"items"`
}

// PreparedGovernanceBatch includes both the compact manifest and native displays
// to show together. Keeping displays separate avoids resending retained source
// bytes to the writer; it reconstructs every selected display in its transaction.
type PreparedGovernanceBatch struct {
	Manifest GovernanceBatchManifest  `json:"manifest"`
	Subject  string                   `json:"subject"`
	Reviews  []PreparedGovernanceItem `json:"reviews"`
}

// GovernanceBatchApproval names the reviewed set and only its explicitly
// approved items. A historical local reason does not imply this approval.
type GovernanceBatchApproval struct {
	Manifest                GovernanceBatchManifest   `json:"manifest"`
	ExpectedManifestSubject string                    `json:"expected_manifest_subject"`
	ApprovedItems           []GovernanceBatchDecision `json:"approved_items"`
}

func governanceBatchSubject(m GovernanceBatchManifest) (string, error) {
	if m.Contract != GovernanceBatchV1 || !consistencyText(m.StoreID) || len(m.Items) < 1 || len(m.Items) > 50 {
		return "", fmt.Errorf("batch contract, store and 1..50 manifest items required")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if len(raw) > 256<<10 {
		return "", fmt.Errorf("batch manifest exceeds 256 KiB; prepare a smaller exact set")
	}
	return governanceDigest("governance-batch:sha256:", raw), nil
}

func prepareGovernanceManifest(store string, requests []GovernanceBatchRequest, reviews []PreparedGovernanceItem) (PreparedGovernanceBatch, error) {
	m := GovernanceBatchManifest{Contract: GovernanceBatchV1, StoreID: store, Items: make([]GovernanceBatchManifestItem, len(reviews))}
	for i, item := range reviews {
		entry := GovernanceBatchManifestItem{Record: requests[i].Record, RecordHash: item.RecordHash, Status: item.Status,
			Error: item.Error, Reason: requests[i].Reason, NativeRequestID: item.NativeRequestID}
		if item.Review != nil {
			entry.NativeSubject = item.Review.Subject
			req := item.Review.Display.Request
			entry.NativeRequest = &req
		}
		m.Items[i] = entry
	}
	subject, err := governanceBatchSubject(m)
	return PreparedGovernanceBatch{Manifest: m, Subject: subject, Reviews: reviews}, err
}

func validateGovernanceManifestItem(store string, entry GovernanceBatchManifestItem, decision GovernanceDecision) error {
	if entry.Status != "requires_exact_native_approval" || entry.NativeRequest == nil || entry.Record.StoreID != store {
		return fmt.Errorf("item is not a prepared native operation for this store")
	}
	raw, err := json.Marshal(entry.Record)
	if err != nil {
		return err
	}
	if governanceDigest("sha256:", raw) != entry.RecordHash {
		return fmt.Errorf("historical record fingerprint changed")
	}
	ref, err := json.Marshal(governanceMigrationReference{RecordID: entry.Record.RecordID, Revision: entry.Record.Revision, RecordHash: entry.RecordHash})
	if err != nil {
		return err
	}
	req := normalizeGovernanceRequest(entry.Record.Native)
	req.MigrationRef = string(ref)
	id := governanceDigest("migration:", []byte(store+"\n"+string(ref)+"\n"+entry.NativeSubject))
	if !reflect.DeepEqual(req, *entry.NativeRequest) || entry.NativeRequestID != id ||
		decision.ExpectedSubject != entry.NativeSubject || decision.RequestID != id || decision.Reason != entry.Reason ||
		!reflect.DeepEqual(normalizeGovernanceRequest(decision.Review), req) {
		return fmt.Errorf("approved item differs from the reviewed manifest")
	}
	return nil
}

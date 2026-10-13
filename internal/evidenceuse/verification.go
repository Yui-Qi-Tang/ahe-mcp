package evidenceuse

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

const VerificationV1 = "evidence-verification-cache/v1"

// Material identifies the actual material checked, separately from the original
// retained source. Identity includes the provider namespace, object and role in
// the check; it must be non-secret. A saved hash does not establish freshness.
type Material struct {
	Identity    string `json:"identity"`
	Revision    string `json:"revision"`
	ContentHash string `json:"content_hash"`
}

type VerificationSubject struct {
	StoreID          string `json:"store_id"`
	CanonicalID      string `json:"canonical_id"`
	ClaimHash        string `json:"claim_hash"`
	Criterion        string `json:"criterion"`
	CriterionVersion string `json:"criterion_version"`
	Scope            string `json:"scope"`
}

// VerificationReport is a historical, scoped report, not an exclusion decision
// or an aggregate source-truth label. Incomplete and uncertain checks never hit.
type VerificationReport struct {
	Version           string              `json:"version"`
	Subject           VerificationSubject `json:"subject"`
	Materials         []Material          `json:"materials"`
	MaterialsComplete bool                `json:"materials_complete"`
	Result            CheckResult         `json:"result"`
	CheckedAt         time.Time           `json:"checked_at"`
}

// VerificationReuse deliberately contains no current-use eligibility. Consumers
// still need current governance and source requirements through CheckPlan.
type VerificationReuse struct {
	Reusable bool   `json:"reusable"`
	Reason   string `json:"reason"`
}

// ReuseVerification requires a live recheck for each prospective hit. The
// consumer owns provider access and freshness policy; recheck must actually
// confirm the complete requested material set for this invocation. Returning a
// report's saved hashes is not a conforming implementation. No callback is
// invoked for a mismatched subject or a non-passing historical report.
func ReuseVerification(ctx context.Context, report VerificationReport, subject VerificationSubject, required []Material, recheck func(context.Context, []Material) ([]Material, error)) (VerificationReuse, error) {
	if ctx == nil {
		return VerificationReuse{}, fmt.Errorf("context is required")
	}
	if err := ctx.Err(); err != nil {
		return VerificationReuse{}, err
	}
	for _, field := range []string{subject.StoreID, subject.CanonicalID, subject.ClaimHash, subject.Criterion, subject.CriterionVersion, subject.Scope} {
		if !validText(field) {
			return VerificationReuse{}, fmt.Errorf("complete verification subject is required")
		}
	}
	if report.Version != VerificationV1 || report.Subject != subject || report.Result != CheckPass || report.CheckedAt.IsZero() || !report.MaterialsComplete {
		return VerificationReuse{Reason: "historical_report_not_applicable"}, nil
	}
	want, ok := materialSet(required)
	if !ok {
		return VerificationReuse{}, fmt.Errorf("complete distinct required material coordinates are required")
	}
	previous, ok := materialSet(report.Materials)
	if !ok || !slices.Equal(previous, want) {
		return VerificationReuse{Reason: "material_set_changed"}, nil
	}
	if recheck == nil {
		return VerificationReuse{Reason: "freshness_not_confirmed"}, nil
	}
	observed, err := recheck(ctx, slices.Clone(want))
	if err != nil {
		return VerificationReuse{Reason: "freshness_not_confirmed"}, err
	}
	if err := ctx.Err(); err != nil {
		return VerificationReuse{}, err
	}
	current, ok := materialSet(observed)
	if !ok || !slices.Equal(current, want) {
		return VerificationReuse{Reason: "required_material_changed_or_missing"}, nil
	}
	return VerificationReuse{Reusable: true, Reason: "scoped_semantic_report_reusable_governance_check_still_required"}, nil
}

func materialSet(materials []Material) ([]Material, bool) {
	if len(materials) == 0 || len(materials) > 128 {
		return nil, false
	}
	result := slices.Clone(materials)
	slices.SortFunc(result, func(a, b Material) int { return strings.Compare(a.Identity, b.Identity) })
	for i, m := range result {
		if !validText(m.Identity) || !validText(m.Revision) || !validText(m.ContentHash) || (i > 0 && result[i-1].Identity == m.Identity) {
			return nil, false
		}
	}
	return result, true
}

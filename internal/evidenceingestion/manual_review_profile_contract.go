package evidenceingestion

// These compatibility types support pure relation reviews only. Current intake
// does not create manual review profiles; their native persistence is unported.

import (
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// ManualSourceReviewProfileV1 identifies opt-in metadata bound at first capture.
	ManualSourceReviewProfileV1 = "manual-source-review-profile/v1"
	// ManualReviewCoverageSubmittedText covers this submitted text, not an external document.
	ManualReviewCoverageSubmittedText = "submitted_text"
	// ManualReviewCoverageExactExcerpt describes an explicitly limited excerpt.
	ManualReviewCoverageExactExcerpt = "exact_excerpt"
	// ManualReviewProfileMaxBytes bounds the canonical profile before persistence or decoding.
	ManualReviewProfileMaxBytes = 24 * 1024
	// ManualReviewTitleMaxBytes bounds a manually supplied source title.
	ManualReviewTitleMaxBytes = 1000
	// ManualReviewLocationMaxBytes bounds a manually supplied locator.
	ManualReviewLocationMaxBytes = 2000
	// ManualReviewLimitationMaxBytes bounds one limitation.
	ManualReviewLimitationMaxBytes = 1000
	// ManualReviewLimitationsMaxCount bounds the complete limitations list.
	ManualReviewLimitationsMaxCount = 16
)

// ManualReviewProfileInput is a typed opt-in, not an origin_metadata convention.
// Location describes the submission; it does not authenticate an external source.
type ManualReviewProfileInput struct {
	ContractVersion string   `json:"contract_version"`
	Title           string   `json:"title"`
	Location        string   `json:"location"`
	Coverage        string   `json:"coverage"`
	Limitations     []string `json:"limitations"`
}

// ManualReviewProfileBinding retains the immutable first-capture qualification.
// Native loaders prove persistence; this value alone grants no write authority.
type ManualReviewProfileBinding struct {
	SourceSnapshotID string                   `json:"source_snapshot_id"`
	IntakeRequestID  string                   `json:"intake_request_id"`
	ContractVersion  string                   `json:"contract_version"`
	Profile          ManualReviewProfileInput `json:"profile"`
	ProfileHash      string                   `json:"profile_hash"`
}

// ValidateManualReviewProfileInput validates without normalizing reviewed bytes.
func ValidateManualReviewProfileInput(input ManualReviewProfileInput) error {
	if input.ContractVersion != ManualSourceReviewProfileV1 {
		return newDomainError(ErrorInvalidInput, "manual review profile contract is unsupported")
	}
	for _, field := range []struct {
		name, value string
		limit       int
	}{{"title", input.Title, ManualReviewTitleMaxBytes}, {"location", input.Location, ManualReviewLocationMaxBytes}} {
		if err := validateManualReviewText(field.name, field.value, field.limit); err != nil {
			return err
		}
	}
	if input.Limitations == nil || len(input.Limitations) > ManualReviewLimitationsMaxCount {
		return newDomainError(ErrorInvalidInput, "manual review limitations require an explicit bounded array")
	}
	for _, limitation := range input.Limitations {
		if err := validateManualReviewText("limitation", limitation, ManualReviewLimitationMaxBytes); err != nil {
			return err
		}
	}
	switch input.Coverage {
	case ManualReviewCoverageSubmittedText:
	case ManualReviewCoverageExactExcerpt:
		if len(input.Limitations) == 0 {
			return newDomainError(ErrorInvalidInput, "manual exact excerpt requires a limitation")
		}
	default:
		return newDomainError(ErrorInvalidInput, "manual review coverage is unsupported")
	}
	data, err := deterministicJSON(input)
	if err != nil || len(data) > ManualReviewProfileMaxBytes {
		return newDomainError(ErrorInvalidInput, "manual review profile exceeds its canonical byte bound")
	}
	return nil
}

func validateManualReviewText(name, value string, limit int) error {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || strings.TrimSpace(value) != value {
		return newDomainError(ErrorInvalidInput, "manual review %s must be nonempty bounded normalized UTF-8 without NUL", name)
	}
	return nil
}

// ValidateManualReviewProfileBinding checks the profile body and its exact hash.
func ValidateManualReviewProfileBinding(binding ManualReviewProfileBinding) error {
	if !hasStableIDPrefix(binding.SourceSnapshotID, "srcsnap:") || binding.ContractVersion != ManualSourceReviewProfileV1 {
		return newDomainError(ErrorReviewContractConflict, "manual review profile binding has invalid source or contract")
	}
	if err := validateManualReviewText("source snapshot", binding.SourceSnapshotID, 200); err != nil {
		return err
	}
	if err := validateManualReviewText("intake request", binding.IntakeRequestID, 1000); err != nil {
		return err
	}
	if err := ValidateManualReviewProfileInput(binding.Profile); err != nil {
		return err
	}
	data, err := deterministicJSON(binding.Profile)
	if err != nil || contentHash(data) != binding.ProfileHash {
		return newDomainError(ErrorReviewContractConflict, "manual review profile binding hash differs from its exact body")
	}
	return nil
}

// cloneManualReviewProfile detaches optional pure review metadata.
func cloneManualReviewProfile(binding *ManualReviewProfileBinding) *ManualReviewProfileBinding {
	if binding == nil {
		return nil
	}
	cloned := *binding
	cloned.Profile.Limitations = slices.Clone(binding.Profile.Limitations)
	return &cloned
}

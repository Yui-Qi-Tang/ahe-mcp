package sourcepilot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"unicode/utf8"
)

const (
	// BriefParentJSONLimit bounds the explicitly supplied selection source file.
	BriefParentJSONLimit = 1 << 20
	// BriefParentBodyLimit allows selection without relaxing model input limits.
	BriefParentBodyLimit = 512 << 10
	// BriefExcerptVersion identifies exact half-open decoded UTF-8 byte ranges.
	BriefExcerptVersion = "detective-brief-excerpt/v1"
)

// BriefExcerpt records caller-declared parent identity and locally computed
// selection coordinates. Saved coordinates do not prove provider authenticity
// or establish that a reader has compared them with the parent body.
type BriefExcerpt struct {
	Version              string `json:"version"`
	ParentSourceID       string `json:"parent_source_id"`
	ParentSourceRevision string `json:"parent_source_revision"`
	ParentBodySHA256     string `json:"parent_body_sha256"`
	ParentBodyBytes      int    `json:"parent_body_bytes"`
	StartByte            int    `json:"start_byte"`
	EndByte              int    `json:"end_byte"`
	SelectionReason      string `json:"selection_reason"`
}

// ParseBriefSelectionSource reads a bounded, complete parent solely for explicit
// excerpt selection. It does not segment it or make it eligible for model input.
func ParseBriefSelectionSource(raw []byte) (BriefSource, error) {
	if len(raw) > BriefParentJSONLimit {
		return BriefSource{}, &InputLimitError{Resource: "parent_json_bytes", Limit: BriefParentJSONLimit, Observed: int64(len(raw))}
	}
	var source BriefSource
	if guidedReportDecode(raw, &source) != nil {
		return BriefSource{}, errors.New("invalid brief parent source JSON")
	}
	if err := validateBriefParent(source); err != nil {
		return BriefSource{}, err
	}
	return source, nil
}

func validateBriefParent(source BriefSource) error {
	if source.Version != BriefSourceVersion || (source.SourceKind != "news" && source.SourceKind != "public_event") || source.Coverage != "full_document" || source.Excerpt != nil {
		return errors.New("brief excerpt selection requires an explicit v2 news or public_event full_document parent without an excerpt")
	}
	return validateBriefSourceFields(source, BriefParentBodyLimit, "parent_body_bytes")
}

// SelectBriefExcerpt copies exactly one explicit range without trimming or
// rewriting it. Parent segmentation is irrelevant; the selected body must still
// satisfy every existing Brief model input and paragraph bound.
func SelectBriefExcerpt(parent BriefSource, start, end int, reason string) (BriefSource, error) {
	if err := validateBriefParent(parent); err != nil {
		return BriefSource{}, err
	}
	if start < 0 || end <= start || end > len(parent.Body) || !utf8.ValidString(parent.Body[:start]) || !utf8.ValidString(parent.Body[:end]) {
		return BriefSource{}, errors.New("brief excerpt requires a nonempty half-open range on UTF-8 byte boundaries")
	}
	digest := sha256.Sum256([]byte(parent.Body))
	source := parent
	source.Body = parent.Body[start:end]
	source.Coverage = "exact_excerpt"
	// Current full_document sources have no limitations. Copy the validated
	// parent slice explicitly so selection never substitutes source limitations.
	source.Limitations = append(append([]string{}, parent.Limitations...), "Only the explicitly selected UTF-8 byte range is supplied; the parent body is not retained here.")
	source.Excerpt = &BriefExcerpt{Version: BriefExcerptVersion, ParentSourceID: parent.SourceID, ParentSourceRevision: parent.SourceRevision,
		ParentBodySHA256: hex.EncodeToString(digest[:]), ParentBodyBytes: len(parent.Body), StartByte: start, EndByte: end, SelectionReason: reason}
	if err := ValidateBriefSourceForExtraction(source); err != nil {
		return BriefSource{}, err
	}
	return source, nil
}

func validateBriefExcerpt(source BriefSource) error {
	e := source.Excerpt
	if e == nil {
		return nil
	}
	digest, err := hex.DecodeString(e.ParentBodySHA256)
	if source.Version != BriefSourceVersion || source.Coverage != "exact_excerpt" || e.Version != BriefExcerptVersion || e.ParentSourceID != source.SourceID || e.ParentSourceRevision != source.SourceRevision || err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != e.ParentBodySHA256 || e.ParentBodyBytes <= 0 || e.ParentBodyBytes > BriefParentBodyLimit || e.StartByte < 0 || e.EndByte <= e.StartByte || e.EndByte > e.ParentBodyBytes || e.EndByte-e.StartByte != len(source.Body) || !guidedText(e.SelectionReason, 1024, false, false) {
		return errors.New("invalid brief excerpt parent identity, digest, byte range or selection reason")
	}
	return nil
}

// VerifyBriefExcerpt compares saved coordinates and exact text with a supplied
// parent offline. Success proves only this byte relationship, not authenticity,
// semantic support or complete extraction. No verification flag is persisted.
func VerifyBriefExcerpt(parent, source BriefSource) error {
	if err := ValidateBriefSourceForExtraction(source); err != nil {
		return err
	}
	if source.Excerpt == nil {
		return errors.New("brief source has no recorded parent excerpt coordinates")
	}
	e := source.Excerpt
	want, err := SelectBriefExcerpt(parent, e.StartByte, e.EndByte, e.SelectionReason)
	if err != nil {
		return err
	}
	if *want.Excerpt != *e || source.Body != want.Body || source.SourceURL != parent.SourceURL || source.ObservedAt != parent.ObservedAt || source.SourceKind != parent.SourceKind || !slices.Equal(source.Limitations, want.Limitations) {
		return errors.New("brief excerpt does not match the supplied parent identity, digest, range and exact bytes")
	}
	return nil
}

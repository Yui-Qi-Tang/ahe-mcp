package evidenceingestionmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Keep unqualified legacy capture decoding unchanged. Opt-in qualification must
// be unambiguous, and explicit null must not silently become an unqualified source.
func validateManualProfileRequest(payload []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return err
	}
	for name, value := range fields {
		if !strings.EqualFold(name, "manual_review_profile") {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("manual_review_profile must be an object when supplied")
		}
		return uniqueReviewJSONValue(json.NewDecoder(bytes.NewReader(payload)), 0, "manual source qualification")
	}
	return nil
}

// SubmitTextSourceDescription describes first-capture qualification separately
// from claim review; the optional profile never approves evidence itself.
const SubmitTextSourceDescription = "Persist exact UTF-8 source bytes without proposals or canonical admission. For manual_text only, optional manual_review_profile binds its declared title, location, coverage and limitations at the first capture. It cannot be added to an existing snapshot, changed, or omitted on a qualified retry. Use source-only capture followed by extractor submission. These declarations do not authenticate an external provider or prove source truth."

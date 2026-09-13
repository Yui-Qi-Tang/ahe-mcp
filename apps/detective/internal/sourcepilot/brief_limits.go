package sourcepilot

import "fmt"

const (
	// BriefBodyLimit is the unchanged maximum model input body in UTF-8 bytes.
	BriefBodyLimit = 32 << 10
	// BriefSegmentLimit bounds the complete nonempty paragraph projection.
	BriefSegmentLimit = 64
)

// InputLimitError describes an exceeded input bound without content or paths.
// AtLeast is true when a bounded reader stopped before counting all input.
type InputLimitError struct {
	Resource string
	Limit    int64
	Observed int64
	AtLeast  bool
}

func (e *InputLimitError) Error() string {
	resource := "input"
	switch e.Resource {
	case "source_json_bytes", "body_bytes", "nonempty_segments", "parent_json_bytes", "parent_body_bytes":
		resource = e.Resource
	}
	qualifier := ""
	if e.AtLeast {
		qualifier = "at least "
	}
	return fmt.Sprintf("input limit exceeded: %s limit=%d observed=%s%d; no input was truncated", resource, e.Limit, qualifier, e.Observed)
}

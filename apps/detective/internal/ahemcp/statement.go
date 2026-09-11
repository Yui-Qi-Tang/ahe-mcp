package ahemcp

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

type statementContext struct {
	RecordType       string   `json:"record_type"`
	Subject          string   `json:"subject"`
	EpistemicClass   string   `json:"epistemic_class"`
	Status           string   `json:"status"`
	Scope            string   `json:"scope"`
	SelectionState   string   `json:"selection_state"`
	BlockedBy        []string `json:"blocked_by"`
	DoesNotEstablish []string `json:"does_not_establish"`
	Qualifiers       []string `json:"qualifiers"`
}

// ProposalStatement preserves the extractor-versioned native statement bytes.
// Version 0.1.2 carries all classified context alongside the original statement;
// it does not synthesize omitted claims, certify support, or repair model output.
// Legacy inputs retain their exact projection for historical review and replay.
func ProposalStatement(extractor labstatus.ExtractorInfo, record labstatus.Record) (string, error) {
	statement := record.Statement
	if extractor.Name == labstatus.ExtractorName && extractor.Version != "0.1.0" && extractor.Version != "0.1.1" && extractor.Version != "0.1.2" {
		return "", errors.New("unsupported lab-status extractor projection version")
	}
	if extractor.Name == labstatus.ExtractorName && extractor.Version == "0.1.2" {
		if err := labstatus.ValidateStatementForm(record); err != nil {
			return "", err
		}
		context := statementContext{RecordType: record.RecordType, Subject: record.Subject,
			EpistemicClass: record.EpistemicClass, Status: record.Status, Scope: record.Scope,
			SelectionState: record.SelectionState, BlockedBy: record.BlockedBy,
			DoesNotEstablish: record.DoesNotEstablish, Qualifiers: record.Qualifiers}
		body, err := json.Marshal(context)
		var decoded statementContext
		if err != nil || json.Unmarshal(body, &decoded) != nil || !reflect.DeepEqual(context, decoded) {
			return "", errors.New("candidate projection context must preserve exact valid UTF-8")
		}
		statement += "\n\nDetective extraction context (model-classified candidate, not independent verification):\n" + string(body)
	}
	if !validText(statement, 2000) {
		return "", errors.New("complete proposal statement must fit 2000 UTF-8 bytes without NUL; nothing was truncated")
	}
	return statement, nil
}

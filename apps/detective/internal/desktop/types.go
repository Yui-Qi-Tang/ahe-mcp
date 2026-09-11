// Package desktop exposes bounded Detective workflows to the desktop shell.
package desktop

import (
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

// Version identifies the independently testable desktop preview.
const Version = "0.1.0-preview.18"

// Settings holds explicit connection coordinates, never credentials.
type Settings struct {
	Mode           string       `json:"mode"`
	Model          string       `json:"model"`
	BaseURL        string       `json:"baseURL"`
	SourceID       string       `json:"sourceID"`
	IntakeLauncher string       `json:"intakeLauncher"`
	QueryLauncher  string       `json:"queryLauncher"`
	ReviewLauncher string       `json:"reviewLauncher"`
	Connections    []Connection `json:"connections"`
}

// Connection describes one operator-approved source server.
type Connection struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Transport    string   `json:"transport"`
	Command      string   `json:"command"`
	URL          string   `json:"url"`
	AllowedTools []string `json:"allowedTools"`
}

// Tool is the exact discovered source tool, not evidence of read-only authority.
type Tool struct {
	ConnectionID    string `json:"connectionID"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	InputSchemaJSON string `json:"inputSchemaJSON"`
	Digest          string `json:"digest"`
	SchemaSHA256    string `json:"schemaSHA256"`
	InventorySHA256 string `json:"inventorySHA256"`
	ConfigSHA256    string `json:"configSHA256"`
}

// ToolAdvice is an ephemeral model suggestion, never permission to execute.
type ToolAdvice struct {
	ID            string `json:"id"`
	ConnectionID  string `json:"connectionID"`
	Question      string `json:"question"`
	Outcome       string `json:"outcome"`
	Reason        string `json:"reason"`
	Tool          *Tool  `json:"tool,omitempty"`
	ArgumentsJSON string `json:"argumentsJSON"`
	Model         string `json:"model"`
	PromptVersion string `json:"promptVersion"`
	CreatedAt     string `json:"createdAt"`
}

// Message distinguishes deterministic rehearsal from model-generated advice.
type Message struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Text string `json:"text"`
	Kind string `json:"kind"`
	Time string `json:"time"`
}

// Event records bounded operational metadata without raw error or credential logs.
type Event struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Time   string `json:"time"`
}

// SourceView preserves an observed source separately from extracted candidates.
type SourceView struct {
	ID      string         `json:"id"`
	Title   string         `json:"title"`
	Path    string         `json:"path"`
	Kind    string         `json:"kind"`
	RawText string         `json:"rawText"`
	SHA256  string         `json:"sha256"`
	Bytes   int            `json:"bytes"`
	Rows    []SourceRow    `json:"rows"`
	Note    string         `json:"note"`
	Capture *SourceCapture `json:"capture,omitempty"`
}

// SourceArtifact identifies one durably saved capture, not source authenticity.
type SourceArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// SourceCapture exposes the complete MCP capture without parsing display prose.
// It is published only after the raw result, text and receipt have been saved.
type SourceCapture struct {
	CapturedAt string            `json:"capturedAt"`
	Revision   string            `json:"revision"`
	RawResult  SourceArtifact    `json:"rawResult"`
	Receipt    SourceArtifact    `json:"receipt"`
	Inspection *SourceInspection `json:"inspection,omitempty"`
}

// SourceInspection retains recorded call data after an offline consistency check.
// It confers neither tool authority nor source authenticity.
type SourceInspection struct {
	VerifiedAt    string           `json:"verifiedAt"`
	Config        sourcemcp.Config `json:"config"`
	Tool          sourcemcp.Tool   `json:"tool"`
	ArgumentsJSON string           `json:"argumentsJSON"`
	RawResultJSON string           `json:"rawResultJSON"`
}

// SourceRow offers an exact source line, not model-created row coordinates.
type SourceRow struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// CandidateView retains the complete typed candidate and independent DB state.
type CandidateView struct {
	Ordinal        int              `json:"ordinal"`
	Record         labstatus.Record `json:"record"`
	State          string           `json:"state"`
	FailureStage   string           `json:"failureStage"`
	CheckpointPath string           `json:"checkpointPath"`
}

// State is a detached UI projection, never an approval or admission receipt.
type State struct {
	Version     string               `json:"version"`
	Busy        bool                 `json:"busy"`
	Operation   string               `json:"operation"`
	Error       string               `json:"error"`
	Settings    Settings             `json:"settings"`
	Messages    []Message            `json:"messages"`
	Events      []Event              `json:"events"`
	Source      *SourceView          `json:"source"`
	Candidates  []CandidateView      `json:"candidates"`
	Tools       []Tool               `json:"tools"`
	ToolAdvice  *ToolAdvice          `json:"toolAdvice,omitempty"`
	BatchPath   string               `json:"batchPath"`
	BatchDigest string               `json:"batchDigest"`
	DataDir     string               `json:"dataDir"`
	Extraction  *labstatus.RowBatch  `json:"extraction"`
	BatchResult *pending.BatchResult `json:"batchResult"`
	Brief       *BriefWork           `json:"brief,omitempty"`
	Search      *EvidenceSearchView  `json:"search,omitempty"`
	// WorkspaceID labels the validated path; it is not authority or a process ID.
	WorkspaceID string `json:"workspaceID"`
}

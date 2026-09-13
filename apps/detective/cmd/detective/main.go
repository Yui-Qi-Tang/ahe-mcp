package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/option"
	openaimodel "google.golang.org/adk/v2/model/openaimodel"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

const (
	defaultModel    = "gemma4:e4b-it-qat"
	defaultBaseURL  = "http://localhost:11434/v1"
	defaultSections = "Status at a Glance"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "detective: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "task" {
		return runTask(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "query" {
		return runQuery(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "brief" {
		return runBrief(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "claim-check" {
		return runGuided(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "news" {
		return runNews(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "source" {
		return runSource(args[1:], os.Stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "batch" {
		return runBatch(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "review" {
		return runReview(args[1:], os.Stdin, stdout, stderr)
	}
	flags := flag.NewFlagSet("detective", flag.ContinueOnError)
	flags.SetOutput(stderr)

	input := flags.String("input", "", "absolute path to a Lab STATUS.md")
	modelName := flags.String("model", envOrDefault("DETECTIVE_MODEL", defaultModel), "local OpenAI-compatible model name")
	baseURL := flags.String("base-url", envOrDefault("DETECTIVE_BASE_URL", defaultBaseURL), "local OpenAI-compatible base URL")
	sections := flags.String("sections", defaultSections, "comma-separated exact level-two Markdown headings")
	rowChunks := flags.Bool("row-chunks", false, "extract each Markdown table row in the selected section separately")
	rowLine := flags.Int("row-line", 0, "one-based Markdown table row line to extract (requires -row-chunks)")
	statusClause := flags.String("status-clause", "", "exact status-cell clause to extract (requires -row-line)")
	ahePending := flags.Bool("ahe-submit-pending", false, "submit one validated row as an AHE pending proposal")
	aheIngestCommand := flags.String("ahe-ingest-command", envOrDefault("DETECTIVE_AHE_INGEST_COMMAND", ""), "absolute AHE ingest launcher executable that isolates credentials")
	aheSourceID := flags.String("ahe-source-id", "lab-status", "stable AHE source ID for -ahe-submit-pending")
	checkpointPath := flags.String("ahe-checkpoint", "", "absolute path for a new immutable one-candidate checkpoint in a private directory")
	prepareOnly := flags.Bool("ahe-prepare-only", false, "save a checkpoint without contacting AHE")
	resumePath := flags.String("ahe-resume", "", "resume a saved checkpoint without reading the source or calling the model")
	queryCommand := flags.String("ahe-query-command", "", "absolute AHE Query launcher executable for checkpoint readback")
	inspectPath := flags.String("ahe-inspect", "", "inspect a saved checkpoint without source, model, or intake access")
	receiptPath := flags.String("ahe-receipt", "", "private saved resume JSON for read-only inspection with an explicit Query launcher")
	format := flags.String("format", "text", "inspection output format: text or json")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum extraction duration")
	version := flags.Bool("version", false, "print extractor version and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	explicit := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if explicit["ahe-inspect"] {
		var conflicting string
		flags.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "ahe-inspect", "ahe-receipt", "ahe-query-command", "format", "timeout":
			default:
				conflicting = f.Name
			}
		})
		if conflicting != "" {
			return fmt.Errorf("ahe inspect cannot use extraction or write mode flags: %s", conflicting)
		}
		if strings.TrimSpace(*inspectPath) == "" {
			return fmt.Errorf("ahe inspect requires a nonempty checkpoint path")
		}
		if explicit["ahe-receipt"] != explicit["ahe-query-command"] ||
			(explicit["ahe-receipt"] && (strings.TrimSpace(*receiptPath) == "" || strings.TrimSpace(*queryCommand) == "")) {
			return fmt.Errorf("ahe inspect readback requires both a nonempty receipt path and an explicit query launcher command")
		}
		if *format != "text" && *format != "json" {
			return fmt.Errorf("ahe inspect format must be text or json")
		}
		if *timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		inspection, err := pending.Inspect(ctx, *inspectPath, *receiptPath, *queryCommand)
		if err != nil {
			return err
		}
		if *format == "json" {
			return writeJSON(stdout, inspection)
		}
		return pending.WriteInspectionText(stdout, inspection)
	}
	if explicit["format"] || explicit["ahe-receipt"] {
		return fmt.Errorf("format and ahe receipt flags require ahe inspect mode")
	}
	if (explicit["ahe-resume"] && *resumePath == "") || (explicit["ahe-checkpoint"] && *checkpointPath == "") {
		return fmt.Errorf("ahe resume and checkpoint flags require a nonempty path")
	}
	if *version {
		_, err := fmt.Fprintf(stdout, "%s %s (%s)\n", labstatus.ExtractorName, labstatus.ExtractorVersion, labstatus.SchemaVersion)
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *resumePath != "" {
		var conflicting string
		flags.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "ahe-resume", "ahe-ingest-command", "ahe-query-command", "timeout":
			default:
				conflicting = f.Name
			}
		})
		if conflicting != "" {
			return fmt.Errorf("ahe resume cannot override saved extraction inputs or mode: %s", conflicting)
		}
		if !explicit["ahe-ingest-command"] || *aheIngestCommand == "" || *queryCommand == "" {
			return fmt.Errorf("ahe resume requires ingest and query launcher commands")
		}
		result, err := pending.Resume(ctx, *resumePath, *aheIngestCommand, *queryCommand)
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	if *modelName == "" {
		return fmt.Errorf("model must not be empty")
	}
	if *baseURL == "" {
		return fmt.Errorf("base URL must not be empty")
	}
	sectionNames := splitSections(*sections)
	if *rowChunks && len(sectionNames) != 1 {
		return fmt.Errorf("row chunks require exactly one section")
	}
	if *rowLine < 0 {
		return fmt.Errorf("row line must be positive")
	}
	if *rowLine > 0 && !*rowChunks {
		return fmt.Errorf("row line requires row chunks")
	}
	if *statusClause != "" && *rowLine == 0 {
		return fmt.Errorf("status clause requires row line")
	}
	if *ahePending && (*rowLine == 0 || !*rowChunks) {
		return fmt.Errorf("ahe pending handoff requires row chunks and one row line")
	}
	if *ahePending && *aheIngestCommand == "" {
		return fmt.Errorf("ahe pending handoff requires AHE ingest command")
	}
	if *prepareOnly && (*checkpointPath == "" || *ahePending) {
		return fmt.Errorf("ahe prepare-only requires a checkpoint and cannot submit pending")
	}
	if *checkpointPath != "" && (!*rowChunks || *rowLine == 0 || (!*prepareOnly && !*ahePending)) {
		return fmt.Errorf("ahe checkpoint requires one row and prepare-only or submit-pending")
	}
	if *checkpointPath != "" && *ahePending && (!explicit["ahe-ingest-command"] || *queryCommand == "") {
		return fmt.Errorf("ahe checkpoint submission requires explicit ingest and query launcher commands")
	}
	if *queryCommand != "" && (*checkpointPath == "" || *prepareOnly) {
		return fmt.Errorf("ahe query command requires checkpoint submission or resume")
	}
	var checkpointWriter *pending.Writer
	if *checkpointPath != "" {
		var err error
		checkpointWriter, err = pending.Reserve(*checkpointPath)
		if err != nil {
			return err
		}
		defer checkpointWriter.Close()
	}

	document, err := labstatus.LoadDocument(*input)
	if err != nil {
		return err
	}
	if err := verifyModelEndpoint(ctx, *baseURL, *modelName); err != nil {
		return fmt.Errorf("model endpoint check failed: %w", err)
	}
	selectedDocument, err := document.SelectSections(sectionNames...)
	if err != nil {
		return err
	}

	llm, err := openaimodel.NewModel(ctx, *modelName, &openaimodel.ClientConfig{
		BaseURL: *baseURL,
		Options: []option.RequestOption{
			// Responses uses reasoning.effort; native /api/chat's think field
			// does not disable thinking on this endpoint.
			option.WithJSONSet("reasoning.effort", "none"),
		},
	})
	if err != nil {
		return fmt.Errorf("create model: %w", err)
	}
	extractor, err := labstatus.NewExtractor(llm)
	if err != nil {
		return err
	}
	if *rowChunks {
		if *rowLine > 0 {
			var batch labstatus.RowBatch
			if *statusClause != "" {
				batch, err = extractor.ExtractTableRowStatusClause(ctx, document, sectionNames[0], *rowLine, *statusClause)
			} else {
				batch, err = extractor.ExtractTableRow(ctx, document, sectionNames[0], *rowLine)
			}
			if err != nil {
				return err
			}
			if checkpointWriter != nil {
				checkpoint, err := pending.New(*aheSourceID, document, batch, *statusClause)
				if err != nil {
					return err
				}
				if err := checkpointWriter.Write(checkpoint); err != nil {
					return err
				}
				if err := checkpointWriter.Close(); err != nil {
					return err
				}
				if *prepareOnly {
					return writeJSON(stdout, preparedOutput{
						SchemaVersion: "detective-pending-prepared/v1",
						State:         "prepared", CheckpointDigest: checkpoint.Digest,
					})
				}
				result, err := pending.Resume(ctx, *checkpointPath, *aheIngestCommand, *queryCommand)
				if err != nil {
					return err
				}
				return writeJSON(stdout, result)
			}
			if *ahePending {
				return submitPending(stdout, ctx, *aheIngestCommand, *aheSourceID, document, batch)
			}
			return writeJSON(stdout, batch)
		}
		batch, err := extractor.ExtractTableRows(ctx, document, sectionNames[0])
		if err != nil {
			return err
		}
		return writeJSON(stdout, batch)
	}

	envelope, err := extractor.Extract(ctx, selectedDocument)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("extraction timed out after %s: %w", timeout.String(), err)
		}
		return err
	}

	return writeJSON(stdout, envelope)
}

type preparedOutput struct {
	SchemaVersion    string `json:"schema_version"`
	State            string `json:"state"`
	CheckpointDigest string `json:"checkpoint_digest"`
}

type pendingHandoffOutput struct {
	SchemaVersion string             `json:"schema_version"`
	Batch         labstatus.RowBatch `json:"batch"`
	Handoff       ahemcp.Handoff     `json:"handoff"`
}

func submitPending(stdout io.Writer, ctx context.Context, command, sourceID string, document *labstatus.Document, batch labstatus.RowBatch) error {
	if len(batch.Rows) != 1 || batch.Rows[0].Status != "validated" || batch.Rows[0].Result == nil {
		return fmt.Errorf("ahe pending handoff requires one validated extracted row")
	}
	result := batch.Rows[0].Result
	if result.Outcome != "extracted" || len(result.Records) == 0 {
		return fmt.Errorf("ahe pending handoff requires at least one extracted record")
	}
	if batch.Extractor.Name == labstatus.ExtractorName && batch.Extractor.Version == "0.1.2" && len(result.Limitations) != 0 {
		return fmt.Errorf("ahe pending handoff cannot omit extraction-wide limitations; retain this result locally")
	}
	handoff, err := ahemcp.Submit(ctx, command, sourceID, document, batch.Extractor, result.Records)
	if err != nil {
		return fmt.Errorf("submit ahe pending proposal: %w", err)
	}
	return writeJSON(stdout, pendingHandoffOutput{
		SchemaVersion: "lab-status-ahe-pending-handoff/v0",
		Batch:         batch,
		Handoff:       handoff,
	})
}

type modelListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func verifyModelEndpoint(ctx context.Context, baseURL, modelName string) error {
	endpoint := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(endpoint, "/v1") {
		endpoint += "/v1"
	}
	endpoint += "/models"

	client := &http.Client{Timeout: 8 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build model endpoint check request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", endpoint, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read endpoint response from %s: %w", endpoint, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("endpoint %s returned status %d: %s", endpoint, response.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload modelListResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("decode model list from %s: %w", endpoint, err)
	}

	for _, available := range payload.Data {
		if available.ID == modelName {
			return nil
		}
	}
	return fmt.Errorf("model %q not found at %s", modelName, endpoint)
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON result: %w", err)
	}
	return nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func splitSections(value string) []string {
	parts := strings.Split(value, ",")
	sections := make([]string, 0, len(parts))
	for _, part := range parts {
		sections = append(sections, strings.TrimSpace(part))
	}
	return sections
}

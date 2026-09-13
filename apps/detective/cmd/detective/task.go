package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

const taskUsage = "detective task inspect -input /absolute/private/task.json -model NAME [-format text|json]\n" +
	"detective task run -input /absolute/private/task.json -model NAME -base-url http://127.0.0.1:11434/v1 -out /absolute/private/new-task-run.json [-timeout 2m] [-format text|json]\n" +
	"detective task read -input /absolute/private/task-run.json [-format text|json]\n" +
	"detective task --version\n" +
	"Input is detective-task-input/v1: version, task and source, preserving all supplied original fields and declared provenance.\n" +
	"Inspect and read are offline. Inspect requires a model name to bind the input, but does not contact it.\n" +
	"Run performs one tool-free local selection; the controller copies original paragraph ranges, never model summaries.\n" +
	"Input must be a private regular file (0600 under 0700), at most 1 MiB; output must be a new private file.\n" +
	"No source collection, MCP, DB, pending, human decision or admission. No retries, cloud fallback or environment model defaults.\n" +
	"Read verifies saved internal consistency, not source authenticity, model execution or semantic completeness.\n" +
	"Failed or interrupted runs retain their record and remain failed; never overwrite them or reuse a decision for a changed task.\n"

type taskRunner func(context.Context, taskextract.Task, taskextract.Source, string, string) (taskextract.Result, error)

func runTask(args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runTaskWithContext(ctx, args, stdout, stderr, desktop.ExtractTask)
}

func runTaskWithContext(ctx context.Context, args []string, stdout, stderr io.Writer, runner taskRunner) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return writeNewsText(stdout, taskUsage)
	}
	if len(args) == 1 && args[0] == "--version" {
		return writeNewsText(stdout, fmt.Sprintf("detective-task %s (%s; %s)\n", taskextract.Version, taskextract.PromptVersion, taskextract.RecordVersion))
	}
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "run" && args[0] != "read") {
		return errors.New("task requires inspect, run or read; use task --help")
	}
	mode := args[0]
	flags := flag.NewFlagSet("detective task "+mode, flag.ContinueOnError)
	inputPath := flags.String("input", "", "explicit private task input or saved run")
	format := flags.String("format", "text", "text or json output")
	var modelName, baseURL, out string
	if mode != "read" {
		flags.StringVar(&modelName, "model", "", "explicit model name; inspect does not contact it")
	}
	timeout := 2 * time.Minute
	if mode == "run" {
		flags.StringVar(&baseURL, "base-url", "", "explicit credential-free literal loopback /v1 endpoint")
		flags.StringVar(&out, "out", "", "new private output file; never overwritten")
		flags.DurationVar(&timeout, "timeout", timeout, "maximum duration, at most two minutes")
	}
	if parseReviewFlags(flags, args[1:]) != nil || *inputPath == "" || (*format != "text" && *format != "json") ||
		ctx == nil || ctx.Err() != nil || timeout <= 0 || timeout > 2*time.Minute {
		return errors.New("task requires supported explicit flags without duplicates and an active context")
	}
	if mode == "read" {
		return readTaskRecord(*inputPath, *format, stdout)
	}
	if !newsModelNameSafe(modelName) || (mode == "run" && (!briefEndpointSafe(baseURL) || out == "" || runner == nil)) {
		return errors.New("task requires an explicit model; run also requires a literal loopback endpoint and new output path")
	}
	raw, err := readSourcePrivateLimit(*inputPath, 1<<20)
	if err != nil {
		return errors.New("task input requires a private regular file at most 1 MiB; no model was called")
	}
	input, err := taskextract.ParseInput(raw)
	if err != nil {
		return errors.New("task input must match detective-task-input/v1; no model was called")
	}
	request, err := taskextract.Prepare(input.Task, input.Source, modelName)
	if err != nil {
		return errors.New("task input has invalid or oversized source, purpose or scope; no model was called")
	}
	if mode == "inspect" {
		if *format == "json" {
			return writeSourceJSON(stdout, taskInspection{
				Version: "detective-task-inspection/v1", InputID: request.InputID(), Model: modelName,
				Task: request.Task(), Source: request.Source(), Units: request.Units(), Scope: request.Scope(),
				HumanReview: "not_reviewed", AuthorityEffect: "none",
			})
		}
		return taskextract.WriteInspectionText(stdout, request)
	}
	if len(request.Units()) == 0 {
		return errors.New("task has no readable supplied source; inspect its missing or empty fields, no model was called")
	}
	// Reserve before inventory; the fresh record is never an AHE checkpoint or
	// a reason to retry an uncertain model call into the same path.
	output, err := reserveNewsOutput(out)
	if err != nil {
		return errors.New("task output requires a new file in an existing private directory; no model was called")
	}
	defer output.close()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, attemptErr := runner(ctx, request.Task(), request.Source(), modelName, baseURL)
	if ctx.Err() != nil {
		attemptErr = ctx.Err()
	}
	record, err := taskextract.NewRecord(request, result, attemptErr)
	if err != nil {
		// Never display or persist a partial/unchecked successful result. Keep
		// a bounded failure record instead, without another model invocation.
		attemptErr = taskextract.ErrReplay
		record, err = taskextract.NewRecord(request, taskextract.Result{}, attemptErr)
	}
	if err != nil {
		return errors.New("task record could not be validated; retain reserved output, do not retry or overwrite")
	}
	if err := output.write(record); err != nil {
		return errors.New("task record publication did not complete; retain output, do not retry or overwrite")
	}
	if err := writeTaskRecord(stdout, record, *format); err != nil {
		return errors.New("task record is saved but terminal output failed; do not repeat extraction")
	}
	if attemptErr != nil || record.Status == "failed" {
		return fmt.Errorf("task attempt did not complete (%s); record saved, no retry or AHE call", record.ErrorCode)
	}
	return nil
}

type taskInspection struct {
	Version         string             `json:"version"`
	InputID         string             `json:"input_id"`
	Model           string             `json:"model"`
	Task            taskextract.Task   `json:"task"`
	Source          taskextract.Source `json:"source"`
	Units           []taskextract.Unit `json:"units"`
	Scope           taskextract.Scope  `json:"scope"`
	HumanReview     string             `json:"human_review"`
	AuthorityEffect string             `json:"authority_effect"`
}

func readTaskRecord(path, format string, stdout io.Writer) error {
	raw, err := readSourcePrivateLimit(path, 2<<20)
	if err != nil {
		return errors.New("task read requires a bounded private regular record; no model was called")
	}
	record, err := taskextract.ParseRecord(raw)
	if err != nil {
		return errors.New("task saved record is inconsistent or unsupported; no model was called")
	}
	return writeTaskRecord(stdout, record, format)
}

func writeTaskRecord(w io.Writer, record taskextract.Record, format string) error {
	if format == "json" {
		return writeSourceJSON(w, record)
	}
	return taskextract.WriteRecordText(w, record)
}

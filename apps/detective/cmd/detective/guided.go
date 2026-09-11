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
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

const guidedUsage = "detective claim-check inspect -input /absolute/private/bundle.json [-format text|json]\n" +
	"detective claim-check run -input /absolute/private/bundle.json -model NAME -base-url http://127.0.0.1:11434/v1 -out /absolute/private/new-report.json [-timeout 6m] [-format text|json]\n" +
	"detective claim-check read -input /absolute/private/saved-report.json [-format text|json]\n" +
	"detective claim-check inspect-fixed -input /absolute/private/fixed-claims.json [-format text|json]\n" +
	"detective claim-check check-fixed -input /absolute/private/fixed-claims.json -model NAME -base-url http://127.0.0.1:11434/v1 -out /absolute/private/new-report.json [-timeout 2m] [-format text|json]\n" +
	"Inspect is offline. Run uses an explicit frozen summary/body bundle and at most three local model calls.\n" +
	"Read is offline and checks saved-report consistency, not source or model authenticity. Reports are at most 2 MiB.\n" +
	"Inspect-fixed is offline. Check-fixed uses caller-supplied unverified claims and at most one local model call; neither records approval.\n" +
	"No source fetch, retries, cloud fallback, deletion tests, AHE or DB calls. Human review remains required.\n" +
	"Input is at most 64 KiB, mode 0600 in a 0700 directory. Output must be a new private file.\n" +
	"Failed or interrupted attempts retain their partial report; never overwrite or treat them as complete.\n"

type guidedFactory func(context.Context, string, string) (*sourcepilot.GuidedReviewer, error)

// Status records execution, not a human decision or a claim's truth.
type guidedRunReport struct {
	Version   string                   `json:"version"`
	Model     string                   `json:"model"`
	BaseURL   string                   `json:"base_url"`
	StartedAt string                   `json:"started_at"`
	ElapsedMS int64                    `json:"elapsed_ms"`
	Status    string                   `json:"status"`
	ErrorCode string                   `json:"error_code"`
	Result    sourcepilot.GuidedReport `json:"result"`
}

func runGuided(args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runGuidedWithContext(ctx, args, stdout, stderr, desktop.NewGuidedReviewer)
}

func runGuidedWithContext(ctx context.Context, args []string, stdout, stderr io.Writer, factory guidedFactory) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(stdout, guidedUsage)
		return err
	}
	if len(args) > 0 {
		switch args[0] {
		case "read":
			return readGuidedRun(ctx, args[1:], stdout)
		case "inspect-fixed", "check-fixed":
			return runFixedWithContext(ctx, args, stdout, factory)
		}
	}
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "run") {
		return errors.New("claim-check requires inspect, run, read, inspect-fixed or check-fixed; use claim-check --help")
	}
	runModel := args[0] == "run"
	flags := flag.NewFlagSet("detective claim-check", flag.ContinueOnError)
	input := flags.String("input", "", "explicit private summary/body bundle")
	format := flags.String("format", "text", "text or json output")
	var modelName, baseURL, out string
	timeout := 6 * time.Minute
	if runModel {
		flags.StringVar(&modelName, "model", "", "explicit local model, no environment default")
		flags.StringVar(&baseURL, "base-url", "", "explicit credential-free loopback /v1 endpoint")
		flags.StringVar(&out, "out", "", "new private output file; never overwritten")
		flags.DurationVar(&timeout, "timeout", timeout, "maximum total duration, at most six minutes")
	}
	if parseReviewFlags(flags, args[1:]) != nil || (*format != "text" && *format != "json") || ctx == nil || ctx.Err() != nil || timeout <= 0 || timeout > 6*time.Minute {
		return errors.New("claim-check flags require explicit supported values without duplicates")
	}
	if *input == "" || (runModel && (modelName == "" || baseURL == "" || out == "" || factory == nil)) {
		return errors.New("claim-check requires input; run also requires model, base-url and a new output path")
	}
	if runModel && (!newsEndpointSafe(baseURL) || !newsModelNameSafe(modelName)) {
		return errors.New("claim-check model settings require safe explicit values; no model was called")
	}
	raw, err := readSourcePrivate(*input)
	if err != nil {
		return errors.New("claim-check input requires a bounded private regular file; no model was called")
	}
	source, err := sourcepilot.ParseGuidedSource(raw)
	if err != nil {
		return errors.New("claim-check input is not a valid frozen summary/body bundle; no model was called")
	}
	initial, err := sourcepilot.NewGuidedReport(source)
	if err != nil {
		return errors.New("claim-check input could not be projected; no model was called")
	}
	if !runModel {
		if *format == "json" {
			return writeSourceJSON(stdout, initial)
		}
		return sourcepilot.WriteGuidedText(stdout, initial)
	}
	// Reserving first prevents an existing or unsafe path from causing a model
	// inventory request. All attempts then retain their original source bundle.
	output, err := reserveNewsOutput(out)
	if err != nil {
		return errors.New("claim-check output requires a new file in an existing private directory; no model was called")
	}
	defer output.close()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	report := guidedRunReport{Version: "detective-claim-check-run/v1", Model: modelName, BaseURL: baseURL,
		StartedAt: started.UTC().Format(time.RFC3339Nano), Status: "failed", Result: initial}
	reviewer, attemptErr := factory(ctx, modelName, baseURL)
	if attemptErr != nil || reviewer == nil {
		attemptErr = errors.New("model unavailable")
		report.ErrorCode = "model_unavailable"
	} else {
		report.Result, attemptErr = reviewer.Review(ctx, source)
		if attemptErr != nil {
			report.ErrorCode = "review_failed"
		} else {
			report.Status = "structurally_validated"
		}
	}
	if ctx.Err() != nil {
		attemptErr = ctx.Err()
		report.Status, report.ErrorCode = "failed", "cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			report.ErrorCode = "timeout"
		}
	}
	report.ElapsedMS = time.Since(started).Milliseconds()
	if err := output.write(report); err != nil {
		return errors.New("claim-check report publication did not complete; retain output for inspection, do not retry or overwrite")
	}
	if *format == "json" {
		err = writeSourceJSON(stdout, report)
	} else {
		err = writeGuidedRunText(stdout, report, out)
	}
	if err != nil {
		return errors.New("claim-check report is saved but terminal output failed; do not repeat the run")
	}
	if attemptErr != nil {
		return fmt.Errorf("claim-check did not complete (%s); partial report saved, no retry or AHE call", report.ErrorCode)
	}
	return nil
}

func writeGuidedRunText(w io.Writer, report guidedRunReport, path string) error {
	header := fmt.Sprintf("處理狀態（非人工判定）：%s\n錯誤代碼：%s\n保存檔：%s\n耗時：%d ms\n", report.Status, report.ErrorCode, newsQuoted(path), report.ElapsedMS)
	if err := writeNewsText(w, header); err != nil {
		return err
	}
	return sourcepilot.WriteGuidedText(w, report.Result)
}

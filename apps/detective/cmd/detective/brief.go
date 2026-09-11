package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

const briefRunVersion = "detective-brief-run/v1"

const briefUsage = "detective brief inspect -input /absolute/private/source.json [-format text|json]\n" +
	"detective brief run -input /absolute/private/source.json -model NAME -base-url http://127.0.0.1:11434/v1 -out /absolute/private/new-report.json [-timeout 2m] [-format text|json]\n" +
	"detective brief read -input /absolute/private/saved-report.json [-format text|json]\n" +
	"detective brief --version\n" +
	"Inspect and read are offline. Run makes one tool-free local model attempt to summarize the complete supplied body.\n" +
	"The complete source is retained as a source-level reference, not proof that each summary claim is supported.\n" +
	"Input is at most 64 KiB, mode 0600 under a 0700 directory. Output must be a new private file; saved reports are at most 2 MiB.\n" +
	"No source fetch, retries, cloud fallback, AHE, DB or admission. Human review is still required.\n" +
	"Failed or interrupted attempts retain their report; never overwrite or treat them as complete.\n"

type briefFactory func(context.Context, string, string) (*sourcepilot.BriefExtractor, error)

// A saved run describes local execution, not source authenticity or approval.
type briefRunReport struct {
	Version   string                  `json:"version"`
	Model     string                  `json:"model"`
	BaseURL   string                  `json:"base_url"`
	StartedAt string                  `json:"started_at"`
	ElapsedMS int64                   `json:"elapsed_ms"`
	Status    string                  `json:"status"`
	ErrorCode string                  `json:"error_code"`
	Result    sourcepilot.BriefReport `json:"result"`
}

func runBrief(args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runBriefWithContext(ctx, args, stdout, stderr, desktop.NewBriefExtractor)
}

func runBriefWithContext(ctx context.Context, args []string, stdout, stderr io.Writer, factory briefFactory) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return writeNewsText(stdout, briefUsage)
	}
	if len(args) == 1 && args[0] == "--version" {
		return writeNewsText(stdout, fmt.Sprintf("detective-brief %s (%s)\n", sourcepilot.BriefVersion, sourcepilot.BriefPromptVersion))
	}
	if len(args) > 0 && args[0] == "read" {
		return readBriefRun(ctx, args[1:], stdout)
	}
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "run") {
		return errors.New("brief requires inspect, run or read; use brief --help")
	}
	runModel := args[0] == "run"
	flags := flag.NewFlagSet("detective brief", flag.ContinueOnError)
	input := flags.String("input", "", "explicit private frozen source")
	format := flags.String("format", "text", "text or json output")
	var modelName, baseURL, out string
	timeout := 2 * time.Minute
	if runModel {
		flags.StringVar(&modelName, "model", "", "explicit local model, no environment default")
		flags.StringVar(&baseURL, "base-url", "", "explicit credential-free literal loopback /v1 endpoint")
		flags.StringVar(&out, "out", "", "new private output file; never overwritten")
		flags.DurationVar(&timeout, "timeout", timeout, "maximum duration, at most two minutes")
	}
	if parseReviewFlags(flags, args[1:]) != nil || (*format != "text" && *format != "json") || ctx == nil || ctx.Err() != nil || timeout <= 0 || timeout > 2*time.Minute {
		return errors.New("brief requires supported explicit flags without duplicates and an active context")
	}
	if *input == "" || (runModel && (modelName == "" || baseURL == "" || out == "" || factory == nil)) {
		return errors.New("brief requires input; run also requires model, base-url and a new output path")
	}
	if runModel && (!briefEndpointSafe(baseURL) || !newsModelNameSafe(modelName)) {
		return errors.New("brief requires safe explicit model settings and a literal loopback endpoint; no model was called")
	}
	raw, err := readSourcePrivate(*input)
	if err != nil {
		return errors.New("brief source requires a private regular file at most 64 KiB; no model was called")
	}
	source, err := sourcepilot.ParseBriefSource(raw)
	if err != nil {
		return errors.New("brief source is not a valid frozen source bundle; no model was called")
	}
	initial, err := sourcepilot.NewBriefReport(source)
	if err != nil {
		return errors.New("brief source could not be projected; no model was called")
	}
	if !runModel {
		if *format == "json" {
			return writeSourceJSON(stdout, initial)
		}
		return sourcepilot.WriteBriefText(stdout, initial)
	}
	// An unsafe or existing output must be rejected before model inventory.
	output, err := reserveNewsOutput(out)
	if err != nil {
		return errors.New("brief output requires a new file in an existing private directory; no model was called")
	}
	defer output.close()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	report := briefRunReport{Version: briefRunVersion, Model: modelName, BaseURL: baseURL,
		StartedAt: started.UTC().Format(time.RFC3339Nano), Status: "failed", Result: initial}
	extractor, attemptErr := factory(ctx, modelName, baseURL)
	if attemptErr != nil || extractor == nil {
		attemptErr = errors.New("model unavailable")
		report.ErrorCode = "model_unavailable"
	} else {
		report.Result, attemptErr = extractor.Extract(ctx, source)
		if attemptErr != nil {
			report.ErrorCode = "extraction_failed"
		} else {
			report.Status = "completed_unreviewed"
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
		return errors.New("brief report publication did not complete; retain output for inspection, do not retry or overwrite")
	}
	if *format == "json" {
		err = writeSourceJSON(stdout, report)
	} else {
		err = writeBriefRunText(stdout, report, out)
	}
	if err != nil {
		return errors.New("brief report is saved but terminal output failed; do not repeat extraction")
	}
	if attemptErr != nil {
		return fmt.Errorf("brief attempt did not complete (%s); report saved, no retry or AHE call", report.ErrorCode)
	}
	return nil
}

func briefEndpointSafe(value string) bool {
	if !newsEndpointSafe(value) {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && net.ParseIP(u.Hostname()).IsLoopback()
}

func writeBriefRunText(w io.Writer, report briefRunReport, path string) error {
	header := fmt.Sprintf("處理狀態（非人工判定）：%s\n錯誤代碼：%s\n保存檔：%s\n耗時：%d ms\n", report.Status, report.ErrorCode, newsQuoted(path), report.ElapsedMS)
	if err := writeNewsText(w, header); err != nil {
		return err
	}
	return sourcepilot.WriteBriefText(w, report.Result)
}

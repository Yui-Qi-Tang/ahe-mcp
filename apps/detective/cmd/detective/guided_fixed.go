package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

// Fixed runs do not impersonate the three-stage summary-guided experiment.
type fixedRunReport struct {
	Version   string                  `json:"version"`
	Model     string                  `json:"model"`
	BaseURL   string                  `json:"base_url"`
	StartedAt string                  `json:"started_at"`
	ElapsedMS int64                   `json:"elapsed_ms"`
	Status    string                  `json:"status"`
	ErrorCode string                  `json:"error_code"`
	Result    sourcepilot.FixedReport `json:"result"`
}

func runFixedWithContext(ctx context.Context, args []string, stdout io.Writer, factory guidedFactory) error {
	runModel := args[0] == "check-fixed"
	flags := flag.NewFlagSet("detective claim-check fixed", flag.ContinueOnError)
	input := flags.String("input", "", "explicit private caller-supplied unverified claims")
	format := flags.String("format", "text", "text or json output")
	var modelName, baseURL, out string
	timeout := 2 * time.Minute
	if runModel {
		flags.StringVar(&modelName, "model", "", "explicit local model, no environment default")
		flags.StringVar(&baseURL, "base-url", "", "explicit credential-free loopback /v1 endpoint")
		flags.StringVar(&out, "out", "", "new private output file; never overwritten")
		flags.DurationVar(&timeout, "timeout", timeout, "maximum total duration, at most two minutes")
	}
	if parseReviewFlags(flags, args[1:]) != nil || (*format != "text" && *format != "json") || ctx == nil || ctx.Err() != nil || timeout <= 0 || timeout > 2*time.Minute {
		return errors.New("claim-check fixed flags require explicit supported values without duplicates")
	}
	if *input == "" || (runModel && (modelName == "" || baseURL == "" || out == "" || factory == nil)) {
		return errors.New("claim-check fixed requires input; check-fixed also requires model, base-url and a new output path")
	}
	if runModel && (!newsEndpointSafe(baseURL) || !newsModelNameSafe(modelName)) {
		return errors.New("claim-check fixed model settings require safe explicit values; no model was called")
	}
	raw, err := readSourcePrivate(*input)
	if err != nil {
		return errors.New("claim-check fixed input requires a bounded private regular file; no model was called")
	}
	inputClaims, err := sourcepilot.ParseFixedClaims(raw)
	if err != nil {
		return errors.New("claim-check fixed input is not a valid frozen claims bundle; no model was called")
	}
	initial, err := sourcepilot.NewFixedReport(inputClaims)
	if err != nil {
		return errors.New("claim-check fixed input could not be projected; no model was called")
	}
	if !runModel {
		if *format == "json" {
			return writeSourceJSON(stdout, initial)
		}
		return sourcepilot.WriteFixedText(stdout, initial)
	}
	output, err := reserveNewsOutput(out)
	if err != nil {
		return errors.New("claim-check fixed output requires a new file in an existing private directory; no model was called")
	}
	defer output.close()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	report := fixedRunReport{Version: fixedRunVersion, Model: modelName, BaseURL: baseURL,
		StartedAt: started.UTC().Format(time.RFC3339Nano), Status: "failed", Result: initial}
	reviewer, attemptErr := factory(ctx, modelName, baseURL)
	if attemptErr != nil || reviewer == nil {
		attemptErr = errors.New("model unavailable")
		report.ErrorCode = "model_unavailable"
	} else {
		report.Result, attemptErr = reviewer.CheckFixed(ctx, inputClaims)
		if attemptErr != nil {
			report.ErrorCode = "check_failed"
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
		return errors.New("claim-check fixed report publication did not complete; retain output for inspection, do not retry or overwrite")
	}
	if *format == "json" {
		err = writeSourceJSON(stdout, report)
	} else {
		err = writeFixedRunText(stdout, report, out)
	}
	if err != nil {
		return errors.New("claim-check fixed report is saved but terminal output failed; do not repeat the run")
	}
	if attemptErr != nil {
		return fmt.Errorf("claim-check fixed did not complete (%s); partial report saved, no retry or AHE call", report.ErrorCode)
	}
	return nil
}

func writeFixedRunText(w io.Writer, report fixedRunReport, path string) error {
	header := fmt.Sprintf("固定主張處理狀態（非人工判定）：%s\n錯誤代碼：%s\n保存檔：%s\n耗時：%d ms\n", report.Status, report.ErrorCode, newsQuoted(path), report.ElapsedMS)
	if err := writeNewsText(w, header); err != nil {
		return err
	}
	return sourcepilot.WriteFixedText(w, report.Result)
}

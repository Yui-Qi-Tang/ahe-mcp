package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

func runSourceInspect(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("detective source inspect", flag.ContinueOnError)
	receipt := flags.String("receipt", "", "absolute saved private source receipt")
	includeContent := flags.Bool("include-content", false, "include exact saved source text and raw result JSON")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum offline inspection duration, at most two minutes")
	// A bare flag is an explicit content request only in this new mode. Retain
	// the existing parser's closed flag set and duplicate-flag checks.
	normalized := append([]string(nil), args...)
	for i, arg := range normalized {
		if arg == "-include-content" || arg == "--include-content" {
			normalized[i] = arg + "=true"
		}
	}
	if err := parseReviewFlags(flags, normalized); err != nil {
		return errors.New("source inspect flags require explicit supported values and must not repeat")
	}
	if !filepath.IsAbs(*receipt) || filepath.Clean(*receipt) != *receipt {
		return errors.New("source inspect requires an explicit clean absolute saved receipt path")
	}
	if *timeout <= 0 || *timeout > 2*time.Minute || ctx == nil || ctx.Err() != nil {
		return errors.New("source inspect requires an active context and a timeout greater than zero and at most two minutes")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	source, err := desktop.InspectSource(ctx, *receipt)
	if err != nil || ctx.Err() != nil || source.Capture == nil || source.Capture.Inspection == nil {
		return errors.New("source inspect could not match the saved receipt and artifacts; no connection or file write was made")
	}
	inspection := source.Capture.Inspection
	result := sourceInspection{SchemaVersion: "detective-source-inspection/v1", State: "matched_saved_receipt", VerifiedAt: inspection.VerifiedAt, CapturedAt: source.Capture.CapturedAt, Revision: source.Capture.Revision,
		Text: desktop.SourceArtifact{Path: source.Path, SHA256: source.SHA256, Bytes: source.Bytes}, RawResult: source.Capture.RawResult, Receipt: source.Capture.Receipt,
		Config: inspection.Config, Tool: inspection.Tool, ArgumentsJSON: inspection.ArgumentsJSON, AuthorityEffect: "none",
		Limitations: []string{
			"Only consistency with the selected local receipt was checked; no external authenticity proof or signature.",
			"Historical inventory digest format only; the complete historical inventory was not reverified.",
			"No current upstream state, source completeness, or human approval was established.",
		}}
	if *includeContent {
		result.RawText = &source.RawText
		result.RawResultJSON = &inspection.RawResultJSON
	}
	if err := writeSourceJSON(stdout, result); err != nil {
		return errors.New("source inspection output failed; saved files are unchanged and no connection was made")
	}
	return nil
}

type sourceInspection struct {
	SchemaVersion   string                 `json:"schema_version"`
	State           string                 `json:"state"`
	VerifiedAt      string                 `json:"verified_at"`
	CapturedAt      string                 `json:"captured_at"`
	Revision        string                 `json:"revision"`
	Text            desktop.SourceArtifact `json:"text"`
	RawResult       desktop.SourceArtifact `json:"raw_result"`
	Receipt         desktop.SourceArtifact `json:"receipt"`
	Config          sourcemcp.Config       `json:"config"`
	Tool            sourcemcp.Tool         `json:"tool"`
	ArgumentsJSON   string                 `json:"args_json"`
	RawText         *string                `json:"raw_text,omitempty"`
	RawResultJSON   *string                `json:"raw_result_json,omitempty"`
	AuthorityEffect string                 `json:"authority_effect"`
	AHESubmitted    bool                   `json:"ahe_submitted"`
	Limitations     []string               `json:"limitations"`
}

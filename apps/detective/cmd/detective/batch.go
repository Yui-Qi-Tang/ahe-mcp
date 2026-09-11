package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

// runBatch consumes saved extraction material only. It has no model, provider,
// review-decision or admission flags, and never falls back to normal extraction.
func runBatch(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("batch requires prepare, resume, or inspect")
	}
	mode := args[0]
	if mode != "prepare" && mode != "resume" && mode != "inspect" {
		return errors.New("batch requires prepare, resume, or inspect")
	}
	flags := flag.NewFlagSet("detective batch "+mode, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	indexPath := flags.String("index", "", "private immutable batch index path")
	var sourcePath, rowBatchPath, sourceID, statusClause, intakeCommand, queryCommand *string
	if mode == "prepare" {
		sourcePath = flags.String("input", "", "exact frozen source used by the saved extraction")
		rowBatchPath = flags.String("row-batch", "", "private saved single-row extraction JSON")
		sourceID = flags.String("source-id", "lab-status", "stable source identity")
		statusClause = flags.String("status-clause", "", "exact clause used by the saved extraction, if any")
	} else {
		queryCommand = flags.String("query-command", "", "explicit Query launcher")
		if mode == "resume" {
			intakeCommand = flags.String("ingest-command", "", "explicit intake launcher")
		}
	}
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum batch operation duration")
	if parseReviewFlags(flags, args[1:]) != nil {
		return errors.New("invalid batch arguments; select only flags for the explicit operation")
	}
	if *indexPath == "" || *timeout <= 0 {
		return errors.New("batch requires an index path and a positive timeout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if mode == "prepare" {
		if *sourcePath == "" || *rowBatchPath == "" || *sourceID == "" {
			return errors.New("batch prepare requires source, saved row batch, and source identity")
		}
		if _, err := pending.PrepareBatchIndex(*indexPath, *sourcePath, *rowBatchPath, *sourceID, *statusClause); err != nil {
			return err
		}
		result, err := pending.InspectBatch(ctx, *indexPath, "")
		if err != nil {
			return err
		}
		result.State = "prepared"
		return writeJSON(stdout, result)
	}
	var result pending.BatchResult
	var err error
	if mode == "resume" {
		if *intakeCommand == "" || *queryCommand == "" {
			return errors.New("batch resume requires explicit intake and Query launchers")
		}
		result, err = pending.ResumeBatch(ctx, *indexPath, *intakeCommand, *queryCommand)
	} else {
		result, err = pending.InspectBatch(ctx, *indexPath, *queryCommand)
	}
	// A bounded partial batch report is intentional output, never a successful
	// receipt. Members explicitly distinguish failure from untouched candidates.
	if result.SchemaVersion != "" {
		if writeErr := writeJSON(stdout, result); writeErr != nil {
			return writeErr
		}
	}
	return err
}

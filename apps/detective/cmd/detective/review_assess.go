package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

func runReviewAssess(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("detective review assess", flag.ContinueOnError)
	decision := flags.String("decision", "", "existing private immutable decision path")
	endpoint := flags.String("base-url", "", "explicit loopback Ollama base URL")
	model := flags.String("model", "", "exact installed local model name")
	out := flags.String("out", "", "new private immutable advisory path")
	think := flags.Bool("think", false, "explicit local thinking mode: -think=true or -think=false; default false")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum local assessment duration, at most five minutes")
	if err := parseReviewFlags(flags, args); err != nil {
		return err
	}
	if err := requireReviewPaths(*decision, *out); err != nil {
		return err
	}
	if strings.TrimSpace(*model) == "" || strings.TrimSpace(*endpoint) == "" {
		return errors.New("review assess requires an explicit model and local base URL; no environment defaults are used")
	}
	if *timeout <= 0 || *timeout > 5*time.Minute {
		return errors.New("review assess timeout must be positive and at most five minutes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	assessment, err := pending.AssessReviewReasonWithOptions(ctx, *decision, *endpoint, *model, *out, pending.ReasonAssessmentOptions{Think: *think})
	if err != nil {
		return fmt.Errorf("理由評估未取得可保存的有效建議（階段：%s）；原決策與 AHE 未改變，不能把失敗當成通過。請保留既有檔案，不自動改用其他模型或端點", pending.ReasonAssessmentFailureStage(err))
	}
	if err := pending.WriteReasonAssessmentText(stderr, assessment); err != nil {
		return errors.New("理由評估已保存，但顯示未完成；原決策與 AHE 未改變")
	}
	return writeJSON(stdout, struct {
		SchemaVersion   string `json:"schema_version"`
		State           string `json:"state"`
		Digest          string `json:"digest"`
		DecisionDigest  string `json:"decision_digest"`
		FollowUpCount   int    `json:"follow_up_count"`
		AuthorityEffect string `json:"authority_effect"`
	}{"detective-reason-assessment-saved/v1", "advisory_saved", assessment.Digest, assessment.DecisionDigest, len(assessment.Concerns), "none"})
}

func runReviewQuestions(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("detective review questions", flag.ContinueOnError)
	path := flags.String("assessment", "", "saved private immutable advisory path")
	if err := parseReviewFlags(flags, args); err != nil {
		return err
	}
	if err := requireReviewPaths(*path); err != nil {
		return err
	}
	assessment, err := pending.LoadReasonAssessment(*path)
	if err != nil {
		return errors.New("無法載入完整且一致的私有理由評估；未呼叫模型或 MCP")
	}
	if err := pending.WriteReasonAssessmentText(stdout, assessment); err != nil {
		return errors.New("理由評估顯示未完成；未改變原決策或 AHE")
	}
	return nil
}

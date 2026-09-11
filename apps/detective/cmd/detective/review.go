package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

func runReview(args []string, input io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("review requires prepare, decide, assess, questions, or apply")
	}
	switch args[0] {
	case "prepare":
		return runReviewPrepare(args[1:], stdout)
	case "decide":
		return runReviewDecide(args[1:], input, stdout, stderr)
	case "apply":
		return runReviewApply(args[1:], stdout)
	case "assess":
		return runReviewAssess(args[1:], stdout, stderr)
	case "questions":
		return runReviewQuestions(args[1:], stdout)
	default:
		return errors.New("review requires prepare, decide, assess, questions, or apply")
	}
}

func runReviewPrepare(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("detective review prepare", flag.ContinueOnError)
	checkpoint := flags.String("checkpoint", "", "private checkpoint path")
	receipt := flags.String("receipt", "", "private saved resume receipt path")
	reviewCommand := flags.String("review-command", "", "explicit reviewer launcher path")
	queryCommand := flags.String("query-command", "", "explicit Query launcher path")
	out := flags.String("out", "", "new private review package path")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum preparation duration")
	if err := parseReviewFlags(flags, args); err != nil {
		return err
	}
	if err := requireReviewPaths(*checkpoint, *receipt, *reviewCommand, *queryCommand, *out); err != nil {
		return err
	}
	if *timeout <= 0 {
		return errors.New("review timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	bundle, err := pending.PrepareReview(ctx, *checkpoint, *receipt, *reviewCommand, *queryCommand, *out)
	if err != nil {
		return errors.New("review preparation failed; no approval was granted")
	}
	if err := pending.WriteReviewText(stdout, bundle); err != nil {
		return errors.New("review display failed; no approval was granted")
	}
	return nil
}

func runReviewDecide(args []string, input io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("detective review decide", flag.ContinueOnError)
	reviewPath := flags.String("review", "", "saved private review package path")
	out := flags.String("out", "", "new private local decision path")
	if err := parseReviewFlags(flags, args); err != nil {
		return err
	}
	if err := requireReviewPaths(*reviewPath, *out); err != nil {
		return err
	}
	bundle, err := pending.LoadReview(*reviewPath)
	if err != nil {
		return errors.New("review package could not be loaded")
	}
	if err := pending.WriteReviewText(stderr, bundle); err != nil {
		return errors.New("review display failed; no decision was recorded")
	}
	decision, reason, confirmation, err := readReviewDecision(input, stderr, bundle.Review.Display.ID)
	if err != nil {
		return err
	}
	recorded, err := pending.RecordReviewDecision(bundle, decision, reason, confirmation, *out)
	if err != nil {
		return errors.New("local review decision could not be confirmed as saved; no MCP request was made")
	}
	nextAction := "not_executable_by_current_reviewer"
	if decision != "pending" {
		nextAction = "explicit_review_apply"
	}
	return writeJSON(stdout, reviewDecisionRecorded{
		SchemaVersion: "detective-review-decision-recorded/v1", State: "decision_recorded_locally",
		Decision: decision, Digest: recorded.Digest, ReviewDisplayID: confirmation,
		AuthorityEffect: "none", NextAction: nextAction,
	})
}

type reviewDecisionRecorded struct {
	SchemaVersion   string `json:"schema_version"`
	State           string `json:"state"`
	Decision        string `json:"decision"`
	Digest          string `json:"digest"`
	ReviewDisplayID string `json:"review_display_id"`
	AuthorityEffect string `json:"authority_effect"`
	NextAction      string `json:"next_action"`
}

func runReviewApply(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("detective review apply", flag.ContinueOnError)
	decisionPath := flags.String("decision", "", "saved private local decision path")
	reviewCommand := flags.String("review-command", "", "explicit reviewer launcher path")
	queryCommand := flags.String("query-command", "", "explicit Query launcher path")
	confirmation := flags.String("confirm-display", "", "complete display ID selected for execution")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum review execution duration")
	if err := parseReviewFlags(flags, args); err != nil {
		return err
	}
	if err := requireReviewPaths(*decisionPath, *reviewCommand, *queryCommand); err != nil {
		return err
	}
	if strings.TrimSpace(*confirmation) == "" {
		return errors.New("review apply requires the complete confirmed display ID")
	}
	if *timeout <= 0 {
		return errors.New("review timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result, err := pending.ApplyReviewDecision(ctx, *decisionPath, *reviewCommand, *queryCommand, *confirmation)
	if err != nil {
		if errors.Is(err, pending.ErrReviewDecisionNotExecutable) {
			return errors.New("pending 表示本機暫緩，不執行處置；未啟動任何 MCP 行程")
		}
		return errors.New("review apply 未取得成功結果；決策可能已提交，失敗不代表回復原狀。請保留原決策檔，使用相同決策與相同核准 launcher 重試；不需先取得僅接受 pending 的 review")
	}
	return writeJSON(stdout, result)
}

// parseReviewFlags rejects repeated flags before flag.Parse can silently replace
// a selected file, launcher, or confirmation. All review flags require values.
// Boolean values use "=" so they cannot hide the next flag during this scan.
func parseReviewFlags(flags *flag.FlagSet, args []string) error {
	flags.SetOutput(io.Discard)
	seen := make(map[string]bool)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
			return errors.New("review accepts only the selected mode's explicit flags")
		}
		name := strings.TrimPrefix(arg, "-")
		name = strings.TrimPrefix(name, "-")
		name, _, hasValue := strings.Cut(name, "=")
		selected := flags.Lookup(name)
		if selected == nil || seen[name] {
			return errors.New("review flags must be supported by the selected mode and must not repeat")
		}
		seen[name] = true
		if !hasValue {
			if value, ok := selected.Value.(interface{ IsBoolFlag() bool }); ok && value.IsBoolFlag() {
				return errors.New("review boolean flags require an explicit equals value")
			}
			i++
			if i >= len(args) {
				return errors.New("review flags require explicit values")
			}
		}
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("review flag values are invalid")
	}
	return nil
}

func requireReviewPaths(paths ...string) error {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return errors.New("review requires explicit nonempty absolute file and launcher paths")
		}
	}
	return nil
}

func readReviewDecision(input io.Reader, output io.Writer, displayID string) (string, string, string, error) {
	reader := bufio.NewReader(input)
	if _, err := fmt.Fprintln(output, "\n這一步只在指定私有檔案保存決策，不呼叫 MCP，也不寫入 AHE。\n請先讀完上方完整候選、精確引用、範圍與限制。文字提示及非空理由不是語意正確或真人身分認證。\n選擇 admit／audit_only／reject／pending（無預設；空白即停止）："); err != nil {
		return "", "", "", errors.New("review prompt failed; no decision was recorded")
	}
	decision, err := readReviewLine(reader, 32)
	if err != nil {
		return "", "", "", err
	}
	switch decision {
	case "admit", "audit_only", "reject", "pending":
	default:
		return "", "", "", errors.New("review decision must be admit, audit_only, reject, or pending; nothing was recorded")
	}
	if _, err := fmt.Fprintln(output, "請輸入具體理由，對照候選、精確引用及適用範圍（單行，最多 2000 UTF-8 bytes；程式不判斷語意對錯）："); err != nil {
		return "", "", "", errors.New("review prompt failed; no decision was recorded")
	}
	reason, err := readReviewLine(reader, 2000)
	if err != nil {
		return "", "", "", err
	}
	if strings.TrimSpace(reason) != reason {
		return "", "", "", errors.New("review reason must not have leading or trailing whitespace; nothing was recorded")
	}
	if _, err := fmt.Fprintln(output, "最後請輸入上方完整 display ID，確認這份精確內容（不是審查者姓名；不接受簡寫）："); err != nil {
		return "", "", "", errors.New("review prompt failed; no decision was recorded")
	}
	confirmation, err := readReviewLine(reader, 256)
	if err != nil {
		return "", "", "", err
	}
	if confirmation != displayID {
		return "", "", "", errors.New("review display confirmation does not match; nothing was recorded")
	}
	return decision, reason, confirmation, nil
}

func readReviewLine(reader *bufio.Reader, limit int) (string, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return "", errors.New("review input was incomplete or too long; nothing was recorded")
	}
	value := strings.TrimSuffix(string(line[:len(line)-1]), "\r")
	if len(value) > limit || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("review input is invalid or too long; nothing was recorded")
	}
	if strings.TrimSpace(value) == "" {
		return "", errors.New("review stopped without recording a decision")
	}
	return value, nil
}

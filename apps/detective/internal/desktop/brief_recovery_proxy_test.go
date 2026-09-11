package desktop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type briefRecoveryProxyConfig struct {
	Upstream   string `json:"upstream"`
	Fault      string `json:"fault"`
	TargetTool string `json:"target_tool"`
}

// TestBriefRecoveryProxy is a retained test-binary entry point, never a product
// launcher. Only the explicitly selected private synthetic lab can enable it.
func TestBriefRecoveryProxy(t *testing.T) {
	dir := os.Getenv("DETECTIVE_RECOVERY_PROXY_CASE")
	if dir == "" {
		return
	}
	if err := runBriefRecoveryProxy(t.Context(), dir); err != nil {
		os.Exit(2) // No test diagnostics or upstream stderr enter MCP stdout.
	}
	os.Exit(0) // Suppress the test runner's PASS line in the stdio protocol.
}

func briefRecoveryProxyConfigAt(dir string) (briefRecoveryProxyConfig, error) {
	var config briefRecoveryProxyConfig
	buildDir, err := briefRecoveryBuildDirectory()
	if err != nil {
		return config, err
	}
	lab := filepath.Dir(filepath.Dir(dir))
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil || canonical != dir || !briefRecoveryLabCoordinate(lab, buildDir) ||
		!strings.HasPrefix(filepath.Base(filepath.Dir(dir)), "recovery-acceptance.") {
		return config, errors.New("invalid recovery lab coordinate")
	}
	for _, path := range []string{lab, filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedSourceFile(info) {
			return config, errors.New("recovery directory is not private")
		}
	}
	for _, path := range []string{filepath.Join(lab, "lab.marker"), filepath.Join(dir, "proxy.json")} {
		info, err := os.Lstat(path)
		if err != nil || !privateSourceFile(info) {
			return config, errors.New("recovery configuration is not private")
		}
	}
	marker, err := os.ReadFile(filepath.Join(lab, "lab.marker"))
	if err != nil || string(marker) != "ahe-brief-desktop-lab/v1\n" {
		return config, errors.New("wrong recovery lab marker")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "proxy.json"))
	if err != nil || len(raw) > 4096 {
		return config, errors.New("invalid recovery configuration")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF ||
		filepath.Dir(config.Upstream) != lab || filepath.Base(config.Upstream) != "source-claim-reviewer-launcher" ||
		(config.Fault != "before_write_once" && config.Fault != "after_write_once" && config.Fault != "none") ||
		(config.TargetTool != "admit_reviewed_source_claim" && config.TargetTool != "record_reviewed_source_claim_disposition") {
		return config, errors.New("invalid recovery configuration")
	}
	info, err := os.Lstat(config.Upstream)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 || !ownedSourceFile(info) {
		return config, errors.New("invalid recovery upstream")
	}
	return config, nil
}

func runBriefRecoveryProxy(parent context.Context, dir string) (result error) {
	config, err := briefRecoveryProxyConfigAt(dir)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"proxy-events.jsonl", "fault-consumed", "fault-response.json"} {
		info, err := root.Lstat(name)
		if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !privateSourceFile(info) {
			return errors.New("invalid recovery artifact")
		}
	}
	log, err := root.OpenFile("proxy-events.jsonl", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	events := json.NewEncoder(log)
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, config.Upstream)
	cmd.WaitDelay = 2 * time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	// Cancellation interrupts blocked pipe I/O; the defer below always joins the
	// one owned child. Its stderr is discarded, never copied into private logs.
	stopIO := context.AfterFunc(ctx, func() { _ = input.Close(); _ = output.Close(); _ = os.Stdin.Close() })
	defer stopIO()
	defer func() {
		_ = input.Close() // EOF is the normal shutdown signal.
		if result != nil {
			cancel()
		}
		timer := time.AfterFunc(2*time.Second, cancel)
		result = errors.Join(result, cmd.Wait())
		timer.Stop()
	}()
	in, out := bufio.NewScanner(os.Stdin), bufio.NewScanner(output)
	in.Buffer(make([]byte, 65536), 4<<20)
	out.Buffer(make([]byte, 65536), 4<<20)
	for in.Scan() {
		request := append([]byte(nil), in.Bytes()...)
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(request, &message) != nil {
			return errors.New("invalid recovery request")
		}
		event := func(phase string) error {
			return events.Encode(map[string]string{"method": message.Method, "tool": message.Params.Name, "phase": phase})
		}
		_, markerErr := root.Stat("fault-consumed")
		if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
			return markerErr
		}
		fault := message.Method == "tools/call" && message.Params.Name == config.TargetTool && len(message.ID) > 0 && errors.Is(markerErr, os.ErrNotExist)
		consume := func() error {
			file, err := root.OpenFile("fault-consumed", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			return file.Close()
		}
		if fault && config.Fault == "before_write_once" {
			return errors.Join(consume(), event("before_write_dropped"), errors.New("synthetic pre-write interruption"))
		}
		if _, err := input.Write(append(request, '\n')); err != nil {
			return err
		}
		if err := event("forwarded"); err != nil {
			return err
		}
		if len(message.ID) == 0 {
			continue // JSON-RPC notifications deliberately have no response.
		}
		if !out.Scan() {
			return errors.Join(out.Err(), errors.New("recovery upstream ended before response"))
		}
		response := out.Bytes()
		var received struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(response, &received) != nil || !bytes.Equal(received.ID, message.ID) {
			return errors.New("unexpected recovery response")
		}
		if fault && config.Fault == "after_write_once" && briefRecoverySuccessfulWrite(config.TargetTool, request, response) {
			file, err := root.OpenFile("fault-response.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, writeErr := file.Write(append(response, '\n'))
			if err := errors.Join(writeErr, file.Close()); err != nil {
				return err
			}
			return errors.Join(consume(), event("after_write_response_dropped"), errors.New("synthetic lost acknowledgement"))
		}
		if _, err := os.Stdout.Write(append(response, '\n')); err != nil {
			return err
		}
		if err := event("response_delivered"); err != nil {
			return err
		}
	}
	return in.Err()
}

// A received error or malformed acknowledgement is not evidence of a write.
// Leave it visible to the real client and keep the one-shot fault unconsumed.
func briefRecoverySuccessfulWrite(tool string, request, response []byte) bool {
	var call struct {
		Params struct {
			Arguments struct {
				ExpectedSubject struct {
					ReviewSubject struct {
						ProposalOccurrenceID string `json:"proposal_occurrence_id"`
					} `json:"review_subject"`
				} `json:"expected_subject"`
				Decision string `json:"decision"`
				Reason   string `json:"decision_reason"`
			} `json:"arguments"`
		} `json:"params"`
	}
	var rpc struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError           bool                          `json:"isError"`
			Content           []struct{ Type, Text string } `json:"content"`
			StructuredContent map[string]any                `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal(request, &call) != nil || json.Unmarshal(response, &rpc) != nil ||
		(len(rpc.Error) > 0 && string(rpc.Error) != "null") || rpc.Result.IsError ||
		len(rpc.Result.Content) != 1 || rpc.Result.Content[0].Type != "text" {
		return false
	}
	var ack struct {
		Occurrence string   `json:"proposal_occurrence_id"`
		DecisionID string   `json:"admission_decision_id"`
		Outcome    string   `json:"admission_outcome"`
		Canonical  string   `json:"canonical_ref"`
		Raw        []string `json:"raw_evidence_node_ids"`
		Edges      []string `json:"canonical_edge_ids"`
		By         string   `json:"decision_by"`
		Reason     string   `json:"decision_reason"`
		Replayed   *bool    `json:"replayed"`
	}
	payload := []byte(rpc.Result.Content[0].Text)
	var fields map[string]any
	if json.Unmarshal(payload, &ack) != nil || json.Unmarshal(payload, &fields) != nil ||
		(rpc.Result.StructuredContent != nil && !reflect.DeepEqual(fields, rpc.Result.StructuredContent)) ||
		ack.Occurrence == "" || ack.Occurrence != call.Params.Arguments.ExpectedSubject.ReviewSubject.ProposalOccurrenceID ||
		!briefRecoveryHexID(ack.DecisionID, "adm:", 64) || ack.Replayed == nil {
		return false
	}
	switch tool {
	case "admit_reviewed_source_claim":
		if len(fields) != 7 || call.Params.Arguments.Decision != "approved" || ack.Outcome != "admitted" ||
			!briefRecoveryHexID(ack.Canonical, "canon-node:", 16) || len(ack.Raw) == 0 || len(ack.Edges) != len(ack.Raw) {
			return false
		}
		seen := map[string]bool{ack.Canonical: true}
		for i, raw := range ack.Raw {
			edge := ack.Edges[i]
			if !briefRecoveryHexID(raw, "canon-node:", 16) || !briefRecoveryHexID(edge, "canon-edge:", 16) || seen[raw] || seen[edge] {
				return false
			}
			seen[raw], seen[edge] = true, true
		}
		return true
	case "record_reviewed_source_claim_disposition":
		want := call.Params.Arguments.Decision
		if want == "reject" {
			want = "rejected"
		}
		return len(fields) == 6 && (want == "rejected" || want == "audit_only") && ack.Outcome == want &&
			strings.TrimSpace(ack.By) != "" && ack.Reason != "" && ack.Reason == call.Params.Arguments.Reason
	}
	return false
}

func briefRecoveryHexID(id, prefix string, size int) bool {
	_, err := hex.DecodeString(strings.TrimPrefix(id, prefix))
	return err == nil && strings.HasPrefix(id, prefix) && len(id) == len(prefix)+size
}

func TestBriefRecoveryProxyRejectsOutsideLab(t *testing.T) {
	for _, path := range []string{"", ".", "/tmp", t.TempDir()} {
		if _, err := briefRecoveryProxyConfigAt(path); err == nil {
			t.Fatalf("accepted non-lab path %q", path)
		}
	}
}

func TestBriefRecoveryProxySuccessfulWrite(t *testing.T) {
	for _, decision := range []string{"approved", "reject", "audit_only"} {
		t.Run(decision, func(t *testing.T) {
			tool, outcome := "record_reviewed_source_claim_disposition", decision
			ack := map[string]any{"proposal_occurrence_id": "synthetic-occurrence", "admission_decision_id": "adm:" + strings.Repeat("a", 64), "decision_by": "mock:reviewer", "decision_reason": "Synthetic decision.", "replayed": false}
			if decision == "approved" {
				tool, outcome = "admit_reviewed_source_claim", "admitted"
				delete(ack, "decision_by")
				delete(ack, "decision_reason")
				ack["canonical_ref"], ack["raw_evidence_node_ids"], ack["canonical_edge_ids"] = "canon-node:"+strings.Repeat("b", 16), []string{"canon-node:" + strings.Repeat("c", 16)}, []string{"canon-edge:" + strings.Repeat("d", 16)}
			} else if decision == "reject" {
				outcome = "rejected"
			}
			ack["admission_outcome"] = outcome
			request, err := json.Marshal(map[string]any{"params": map[string]any{"arguments": map[string]any{"decision": decision, "decision_reason": "Synthetic decision.", "expected_subject": map[string]any{"review_subject": map[string]string{"proposal_occurrence_id": "synthetic-occurrence"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			for _, scenario := range []string{"success", "rpc_error", "tool_error", "missing_ack", "wrong_outcome", "wrong_occurrence", "structured_mismatch"} {
				t.Run(scenario, func(t *testing.T) {
					payload, err := json.Marshal(ack)
					if err != nil {
						t.Fatal(err)
					}
					if scenario == "missing_ack" {
						payload = []byte(`{}`)
					}
					if scenario == "wrong_outcome" {
						payload = bytes.ReplaceAll(payload, []byte(`"`+outcome+`"`), []byte(`"pending"`))
					}
					if scenario == "wrong_occurrence" {
						payload = bytes.ReplaceAll(payload, []byte("synthetic-occurrence"), []byte("other-occurrence"))
					}
					result := map[string]any{"content": []map[string]string{{"type": "text", "text": string(payload)}}, "isError": scenario == "tool_error"}
					if scenario == "structured_mismatch" {
						result["structuredContent"] = map[string]string{"status": "pending"}
					}
					rpc := map[string]any{"result": result}
					if scenario == "rpc_error" {
						rpc["error"] = map[string]int{"code": -32603}
					}
					response, err := json.Marshal(rpc)
					if err != nil {
						t.Fatal(err)
					}
					if got := briefRecoverySuccessfulWrite(tool, request, response); got != (scenario == "success") {
						t.Fatalf("successful write = %v", got)
					}
				})
			}
		})
	}
}

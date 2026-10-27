//go:build integration

package mcpintegration

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpendpoints"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

// lostHistoricalReply injects a closed output stream at the tool-response frame,
// after dispatch has returned from its committing database transaction.
type lostHistoricalReply struct {
	bytes.Buffer
	dropped bool
}

func (w *lostHistoricalReply) Write(body []byte) (int, error) {
	var frame struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &frame); err != nil {
		return 0, err
	}
	if frame.ID == 2 {
		w.dropped = true
		return 0, io.ErrClosedPipe
	}
	return w.Buffer.Write(body)
}

func TestIntegrationHistoricalLostResponseReplaysWithoutAnotherWrite(t *testing.T) {
	f := newHistoricalProcessFixture(t)
	ctx := f.ctx
	source, parents := historicalProcessSources(t, f)
	req := historicalProcessProposal(t, f, source, parents, "lost-reply-judgment")
	review := relationTool[evidenceingestion.HistoricalDerivationReview](t, f.processes["historical-derivation-reviewer"], mcpendpoints.ToolHistoricalReview, req)
	in := evidenceingestion.HistoricalDerivationDecision{RequestID: "lost-reply-decision", Review: req, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB"}
	login := f.authority.historical
	config, err := pgxpool.ParseConfig(login.dsn)
	if err != nil {
		t.Fatal("invalid synthetic runtime configuration")
	}
	pool, _, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{Role: login.group, Schema: f.authority.schema, Profile: login.profile})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	backend, err := mcpendpoints.NewBackend(pool, runtimeauth.Principal{ID: historicalProcessPrincipal}, mcpendpoints.ProfileHistoricalReviewer, "", "")
	if err != nil {
		t.Fatal(err)
	}
	frames := new(bytes.Buffer)
	encoder := json.NewEncoder(frames)
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": mcpstdio.ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "synthetic-loss-probe", "version": "1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "apply_historical_derivation_decision", "arguments": in}}); err != nil {
		t.Fatal(err)
	}
	server, err := mcpstdio.NewServer("historical-loss-probe", "test", backend)
	if err != nil {
		t.Fatal(err)
	}
	lost := new(lostHistoricalReply)
	if err := server.Serve(ctx, bytes.NewReader(frames.Bytes()), lost); !errors.Is(err, io.ErrClosedPipe) || !lost.dropped {
		t.Fatal("response loss was not injected", err)
	}
	count := func() [4]int64 {
		t.Helper()
		var counts [4]int64
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM canonical_historical_derivation_reviews WHERE proposal_occurrence_id=$1),(SELECT count(*) FROM canonical_graph_nodes WHERE origin_proposal_occurrence_id=$1),(SELECT count(*) FROM admission_decisions WHERE proposal_occurrence_id=$1),(SELECT revision FROM evidence_governance_head WHERE singleton)`, req.ProposalOccurrenceID).Scan(&counts[0], &counts[1], &counts[2], &counts[3]); err != nil {
			t.Fatal(err)
		}
		return counts
	}
	before := count()
	if before[0] != 1 || before[1] != 1 || before[2] != 1 {
		t.Fatal("failure did not occur after the committed write", before)
	}
	stateAfterLoss := historicalProcessState(t, f)
	var output bytes.Buffer
	server, err = mcpstdio.NewServer("historical-loss-retry", "test", backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(ctx, bytes.NewReader(frames.Bytes()), &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var init json.RawMessage
	if err := decoder.Decode(&init); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		ID     int             `json:"id"`
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool                                          `json:"isError"`
			Receipt evidenceingestion.HistoricalDerivationReceipt `json:"structuredContent"`
		} `json:"result"`
	}
	if err := decoder.Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.ID != 2 || len(reply.Error) != 0 || reply.Result.IsError || !reply.Result.Receipt.Replayed || reply.Result.Receipt.Admission == nil {
		t.Fatal("lost response retry did not return its original receipt", reply)
	}
	if count() != before || !reflect.DeepEqual(stateAfterLoss, historicalProcessState(t, f)) {
		t.Fatal("retry wrote another receipt/node or changed the governance head")
	}
}

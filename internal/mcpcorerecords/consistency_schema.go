package mcpcorerecords

import "github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"

func consistencyScopeSchema() map[string]any {
	return object(map[string]any{
		"policy":    map[string]any{"type": "string", "enum": []string{"bound-declarations/cnf-v0", "classified-frontier/cnf-v1"}},
		"namespace": text(512), "scope_ref": text(512), "max_nodes": integer(1, 256), "max_edges": integer(1, 4096),
	})
}

func consistencyReadTools() []mcpstdio.Tool {
	return []mcpstdio.Tool{
		tool("get_consistency_scope", "Read the complete bounded proposition scope and explicit participation policy. Missing parents, hidden rows and limits fail; no logical or semantic verdict is made.", consistencyScopeSchema(), true),
		tool("get_consistency_watch", "Read an exact watch configuration revision; -1 selects latest in this snapshot. Use current_revision for updates. Historical replay is not current validity.", object(map[string]any{"watch_id": text(512), "revision": integer(-1, 9007199254740991)}), true),
		tool("get_consistency_events", "Read durable events after a per-watch cursor. Follow next_cursor while truncated. Completed only means a result was stored, including failed or blocked results; inspect get_consistency_run.", object(map[string]any{"watch_id": text(512), "after": integer(0, 9007199254740991), "limit": integer(1, 256)}), true),
		tool("get_consistency_run", "Read immutable diagnosis, exact inputs, proof manifest and freshly checked validity. matches_snapshot applies only to this read. Inspect outcome and localization separately; no truth or repair claim.", object(map[string]any{"run_id": text(512)}), true),
		tool("get_consistency_artifact", "Read up to 64 KiB of digest-checked bytes as base64, pinned to exact run_id/artifact_id. Keep these IDs for all chunks. Historical artifact bytes do not assert freshness; read the run separately.", object(map[string]any{"run_id": text(512), "artifact_id": text(1024), "offset": integer(0, 67108864), "limit": integer(1, 65536)}), true),
	}
}

func consistencyWriteTool() mcpstdio.Tool {
	atom := object(map[string]any{"subject": text(512), "property": text(512), "context": text(512)})
	literal := object(map[string]any{"atom": atom, "negated": map[string]any{"type": "boolean"}})
	condition := object(map[string]any{"id": text(512), "clauses": array(array(literal, 1000000), 100000)})
	request := object(map[string]any{"scope": consistencyScopeSchema(), "rule_version": text(512), "rules": array(condition, 4096), "evidence": array(condition, 4096)})
	return tool("register_consistency_watch", "Append a watch configuration, pause or correction with expected_revision and a new request_id. External CNF and rules must be supplied; no extraction or admission. Use the operator's engine_id. The separate worker computes later. To retry a stored failure, submit a new revision. Exact retries return historical receipts. Launcher binds recorded_by. Requests are limited to 2 MiB.", object(map[string]any{
		"contract": map[string]any{"type": "string", "enum": []string{"consistency-watch/v1"}},
		"watch_id": text(512), "request_id": text(512), "expected_revision": integer(0, 9007199254740991), "request": request,
		"engine_id": text(512), "max_localization_calls": integer(1, 64), "paused": map[string]any{"type": "boolean"},
	}), false)
}

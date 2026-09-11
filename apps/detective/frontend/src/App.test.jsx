import React from "react";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import App from "./App.jsx";
import { emptyState } from "./bridge.js";

// These explicit test doubles never ship as an application bridge.
function installBridge(initial = emptyState()) {
  let current = initial;
  const bridge = {};
  for (const name of [
    "Snapshot",
    "SaveSettings",
    "LoadDemo",
    "ChooseSource",
    "ChooseBriefSource",
    "ChooseBriefWork",
    "ChooseSourceReceipt",
    "ChooseBatch",
    "SendMessage",
    "Extract",
    "ExtractBrief",
    "PrepareBriefCandidate",
    "SubmitBriefPending",
    "QueryBriefPending",
    "PrepareBriefReview",
    "ApplyBriefReview",
    "SubmitPending",
    "QueryPending",
    "Cancel",
    "DiscoverTools",
    "CallSourceTool",
  ]) {
    bridge[name] = vi.fn(async () => structuredClone(current));
  }
  window.go = { main: { App: bridge } };
  return {
    bridge,
    update: (value) => {
      current = value;
    },
  };
}

function candidate(ordinal) {
  return {
    ordinal,
    state: ordinal === 16 ? "failed" : "candidate",
    failureStage: ordinal === 16 ? "query_readback" : "",
    checkpointPath: `/private/candidate-${ordinal}.json`,
    record: {
      record_type: "capability_state",
      subject: `能力 ${ordinal}`,
      statement: `完整敘述 ${ordinal}`,
      epistemic_class: "claim",
      status: "implemented",
      scope: "runtime_core",
      selection_state: "selected",
      citation: {
        start_line: 4,
        end_line: 4,
        exact_quote: `精確原文 ${ordinal}`,
      },
      blocked_by: [`受阻 ${ordinal}`],
      does_not_establish: [`不能認定部署 ${ordinal}`],
      qualifiers: [`限定條件 ${ordinal}`],
    },
  };
}

function briefDraftState(withReview = false) {
  const state = emptyState();
  state.settings = { ...state.settings, mode: "local", intakeLauncher: "/test/intake", queryLauncher: "/test/query", reviewLauncher: "/test/review" };
  state.brief = {
    model: "test-local-model", path: "/private/draft-work.json", digest: "draft-work-digest", outcome: "candidate",
    report: { body_sha256: "body-digest", input_sha256: "input-digest", stage: "complete", text: "Atlas requests failed.",
      source: { source_id: "atlas-test", source_revision: "frozen-1", source_url: "https://example.test/status", observed_at: "2026-09-11T00:00:00Z", coverage: "full_document", limitations: [], body: "Atlas requests failed.\nOther regions were unaffected." } },
  };
  if (withReview) {
    state.brief.outcome = "review_ready";
    state.brief.submission = { digest: "submission-digest", statement: "Atlas requests failed.", citation: { start_line: 1, end_line: 2, exact_quote: state.brief.report.source.body } };
    state.brief.handoff = { proposal_occurrence_id: "occ:draft" };
    state.brief.review = { display: { review_display_artifact_id: "display:draft", payload_utf8: '{"statement":"Atlas requests failed."}' }, subject: { review_subject: { proposal_occurrence_id: "occ:draft" } } };
  }
  return state;
}

function readyState(count = 2) {
  const state = emptyState();
  state.settings = {
    ...state.settings,
    mode: "local",
    intakeLauncher: "/operator/intake",
    queryLauncher: "/operator/query",
    connections: [
      {
        id: "source-1",
        name: "合成測試來源",
        transport: "stdio",
        command: "/operator/source",
        url: "",
        allowedTools: ["read_status"],
      },
    ],
  };
  state.batchDigest = "exact-complete-batch-digest";
  state.batchPath = "/private/full-batch.json";
  state.candidates = Array.from({ length: count }, (_, index) =>
    candidate(index + 1),
  );
  state.source = {
    id: "source-id",
    title: "STATUS.md",
    path: "/private/source.md",
    kind: "status",
    sha256: "source-hash",
    bytes: 50,
    rawText: "來源原文不會被摘要取代\n<script>不要執行</script>",
    rows: [{ line: 4, text: "| 完整精確資料列 |" }],
    note: "來源 note",
  };
  state.tools = [
    {
      connectionID: "source-1",
      name: "read_status",
      description: "來源工具描述不是權限證明",
      inputSchemaJSON: '{"type":"object"}',
      digest: "tool-digest",
      schemaSHA256: "schema-digest",
      inventorySHA256: "inventory-digest",
      configSHA256: "config-digest",
    },
    {
      connectionID: "source-1",
      name: "write_other",
      description: "",
      inputSchemaJSON: "{}",
      digest: "not-allowed",
    },
  ];
  return state;
}

async function renderReady(state) {
  const installed = installBridge(state);
  render(<App />);
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "新工作" })).toBeEnabled(),
  );
  await waitFor(() => expect(installed.bridge.Snapshot).toHaveBeenCalled());
  return installed;
}

async function approveSourceTool() {
  fireEvent.click(screen.getByRole("button", { name: "資料源與連線" }));
  fireEvent.change(screen.getByLabelText("已套用的伺服器"), {
    target: { value: "source-1" },
  });
  fireEvent.change(screen.getByLabelText("已探索工具"), {
    target: { value: "read_status" },
  });
  fireEvent.click(screen.getByRole("button", { name: /檢查並批准這次呼叫/ }));
  const modal = within(screen.getByRole("dialog"));
  fireEvent.click(modal.getByRole("checkbox"));
  fireEvent.click(modal.getByRole("button", { name: "同意這次工具呼叫" }));
}

function capturedSourceState() {
  const state = readyState(0);
  state.batchPath = "";
  state.batchDigest = "";
  state.source = { ...state.source, kind: "mcp", path: "/private/new-capture.md" };
  state.events = [{
    id: "capture-completed",
    action: "來源工具讀取",
    status: "completed",
    detail: "來源已保存，未送入 pending。",
    time: "",
  }];
  return state;
}

function inspectedReceiptState() {
  const state = emptyState();
  state.source = {
    id: "saved-source-id",
    kind: "mcp",
    title: "已保存來源",
    path: "/private/saved-display.txt",
    sha256: "display-hash",
    bytes: 48,
    rawText: "<script>不要執行</script>\n[連結](https://example.invalid)",
    rows: [],
    capture: {
      capturedAt: "2026-09-09T02:00:00Z",
      revision: "unknown",
      rawResult: {
        path: "/private/raw-result.json",
        sha256: "raw-hash",
        bytes: 128,
      },
      receipt: {
        path: "/private/source-receipt.json",
        sha256: "receipt-hash",
        bytes: 256,
      },
      inspection: {
        verifiedAt: "2026-09-09T03:00:00Z",
        config: {
          id: "historical-source",
          name: '<img src="https://example.invalid/pixel" onerror="admit()">',
          transport: "stdio",
          command: "/historical/launcher",
          allowed_tools: ["read_status"],
        },
        tool: {
          name: "read_status",
          description: "<script>admit()</script>",
          input_schema: { type: "object" },
          schema_sha256: "saved-schema-hash",
          inventory_sha256: "saved-inventory-hash",
          config_sha256: "saved-config-hash",
          digest: "saved-tool-hash",
        },
        argumentsJSON:
          '{\n  "text": "<iframe src=https://example.invalid>", "decision": "admit"\n}\n',
        rawResultJSON:
          '{\n "content": [{"type":"text","text":"<svg onload=admit()></svg>"}]\n}\n',
      },
    },
  };
  state.events = [
    {
      id: "receipt-inspected-1",
      action: "開啟來源收據",
      status: "completed",
      detail: "檔案與已保存收據一致。",
      time: "2026-09-09T03:00:00Z",
    },
  ];
  return state;
}

afterEach(() => vi.useRealTimers());

describe("Desktop interface authority and recovery", () => {
  it("retains an unsaved Brief candidate while checking settings or activity", async () => {
    const state = briefDraftState();
    const { bridge } = await renderReady(state);
    fireEvent.change(screen.getByLabelText("Brief 候選主張"), { target: { value: "My carefully edited claim." } });
    fireEvent.change(screen.getByLabelText("Brief 引文起始行"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Brief 引文結束行"), { target: { value: "2" } });
    for (const page of ["資料源與連線", "執行紀錄", "證據搜尋"]) {
      fireEvent.click(screen.getByRole("button", { name: page, exact: true }));
      fireEvent.click(screen.getByRole("button", { name: "工作台", exact: true }));
      expect(screen.getByLabelText("Brief 候選主張")).toHaveValue("My carefully edited claim.");
      expect(screen.getByLabelText("Brief 引文起始行")).toHaveValue(2);
      expect(screen.getByLabelText("Brief 引文結束行")).toHaveValue(2);
    }
    for (const [name, method] of Object.entries(bridge)) if (name !== "Snapshot") expect(method).not.toHaveBeenCalled();
  });
  it("retains an unsent Brief decision draft across navigation but revokes confirmation", async () => {
    const state = briefDraftState(true);
    const { bridge } = await renderReady(state);
    const reason = "Original source supports only the stated scope.";
    fireEvent.click(screen.getByRole("radio", { name: /^reject ·/ }));
    fireEvent.change(screen.getByLabelText("Brief 本次具體理由"), { target: { value: reason } });
    fireEvent.click(screen.getByRole("button", { name: "檢查本次審查決定" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "執行紀錄", exact: true }));
    fireEvent.click(screen.getByRole("button", { name: "工作台", exact: true }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /^reject ·/ })).toBeChecked();
    expect(screen.getByLabelText("Brief 本次具體理由")).toHaveValue(reason);
    fireEvent.click(screen.getByRole("button", { name: "檢查本次審查決定" }));
    const modal = within(screen.getByRole("dialog"));
    expect(modal.getByRole("checkbox")).not.toBeChecked();
    expect(modal.getByRole("button", { name: "送出本次精確審查決定" })).toBeDisabled();
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
    expect(bridge.PrepareBriefReview).not.toHaveBeenCalled();
  });
  it.each([false, true])("keeps the same Brief draft through checkpoint progress (review=%s)", async (withReview) => {
    const state = briefDraftState(withReview);
    const { bridge } = await renderReady(state);
    if (withReview) {
      fireEvent.click(screen.getByRole("radio", { name: /^audit_only ·/ }));
      fireEvent.change(screen.getByLabelText("Brief 本次具體理由"), { target: { value: "Keep this unsent reason." } });
    } else {
      fireEvent.change(screen.getByLabelText("Brief 候選主張"), { target: { value: "Keep this unsaved claim." } });
      fireEvent.change(screen.getByLabelText("Brief 引文起始行"), { target: { value: "2" } });
      fireEvent.change(screen.getByLabelText("Brief 引文結束行"), { target: { value: "2" } });
    }
    const next = structuredClone(state);
    next.brief.path = "/private/next-checkpoint.json";
    next.brief.digest = "next-checkpoint-digest";
    if (withReview) {
      next.brief.outcome = "pending";
      bridge.QueryBriefPending.mockResolvedValueOnce(next);
      fireEvent.click(screen.getByRole("button", { name: "查回這筆 Brief 狀態" }));
    } else {
      bridge.Snapshot.mockResolvedValueOnce(next);
      fireEvent.click(screen.getByRole("button", { name: "重新同步桌面狀態" }));
    }
    await waitFor(() => expect(screen.getByText("/private/next-checkpoint.json")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "資料源與連線", exact: true }));
    fireEvent.click(screen.getByRole("button", { name: "工作台", exact: true }));
    if (withReview) {
      expect(screen.getByRole("radio", { name: /^audit_only ·/ })).toBeChecked();
      expect(screen.getByLabelText("Brief 本次具體理由")).toHaveValue("Keep this unsent reason.");
    } else {
      expect(screen.getByLabelText("Brief 候選主張")).toHaveValue("Keep this unsaved claim.");
      expect(screen.getByLabelText("Brief 引文起始行")).toHaveValue(2);
      expect(screen.getByLabelText("Brief 引文結束行")).toHaveValue(2);
    }
    expect(bridge.PrepareBriefCandidate).not.toHaveBeenCalled();
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
  });
  it.each(["ChooseBriefSource", "ChooseBriefWork"])("clears the candidate draft after a fresh successful %s, even for identical source content", async (method) => {
    const state = briefDraftState();
    const { bridge } = await renderReady(state);
    fireEvent.change(screen.getByLabelText("Brief 候選主張"), { target: { value: "Draft belonging only to the old work." } });
    fireEvent.change(screen.getByLabelText("Brief 引文起始行"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Brief 引文結束行"), { target: { value: "2" } });
    const opened = structuredClone(state);
    opened.brief.path = "/private/another-same-source-work.json";
    opened.brief.digest = "another-work-digest";
    opened.events.push({ id: "fresh-open", action: method === "ChooseBriefWork" ? "brief_open" : "brief_import", status: "completed", detail: "Synthetic explicit open", time: "" });
    bridge[method].mockResolvedValueOnce(opened);
    fireEvent.click(screen.getByRole("button", { name: method === "ChooseBriefWork" ? "恢復 Brief 工作" : "開啟 Brief 來源" }));
    await waitFor(() => expect(screen.getByText(opened.brief.path)).toBeInTheDocument());
    expect(screen.getByLabelText("Brief 候選主張")).toHaveValue(opened.brief.report.text);
    expect(screen.getByLabelText("Brief 引文起始行")).toHaveValue(null);
    expect(screen.getByLabelText("Brief 引文結束行")).toHaveValue(null);
    expect(screen.getByRole("button", { name: "保存這筆本機候選" })).toBeDisabled();
    expect(bridge.PrepareBriefCandidate).not.toHaveBeenCalled();
  });
  it("drops an unsent decision and its checked confirmation when explicitly reopening the same saved work", async () => {
    const state = briefDraftState(true);
    const { bridge } = await renderReady(state);
    fireEvent.click(screen.getByRole("radio", { name: /^admit ·/ }));
    fireEvent.change(screen.getByLabelText("Brief 本次具體理由"), { target: { value: "Unsent reason from the previous open." } });
    fireEvent.click(screen.getByRole("button", { name: "檢查本次審查決定" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("checkbox"));
    const reopened = structuredClone(state);
    reopened.events.push({ id: "fresh-reopen", action: "brief_open", status: "completed", detail: "Synthetic same-file reopen", time: "" });
    bridge.ChooseBriefWork.mockResolvedValueOnce(reopened);
    fireEvent.click(screen.getByRole("button", { name: "恢復 Brief 工作" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    for (const radio of screen.getAllByRole("radio")) expect(radio).not.toBeChecked();
    expect(screen.getByLabelText("Brief 本次具體理由")).toHaveValue("");
    expect(screen.getByRole("button", { name: "檢查本次審查決定" })).toBeDisabled();
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
  });
  it.each(["cancelled", "failed", "stale-completion"])("retains a Brief draft after a %s work chooser result", async (resultKind) => {
    const state = briefDraftState();
    if (resultKind === "stale-completion") state.events = [{ id: "old-open", action: "brief_open", status: "completed", detail: "Earlier open", time: "" }];
    const { bridge } = await renderReady(state);
    fireEvent.change(screen.getByLabelText("Brief 候選主張"), { target: { value: "Keep the current draft." } });
    fireEvent.change(screen.getByLabelText("Brief 引文起始行"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Brief 引文結束行"), { target: { value: "2" } });
    const retained = structuredClone(state);
    if (resultKind === "failed") {
      retained.error = "Synthetic failed open";
      retained.events.push({ id: "failed-open", action: "brief_open", status: "failed", detail: "Synthetic error", time: "" });
    }
    bridge.ChooseBriefWork.mockResolvedValueOnce(retained);
    fireEvent.click(screen.getByRole("button", { name: "恢復 Brief 工作" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "恢復 Brief 工作" })).toBeEnabled());
    expect(bridge.ChooseBriefWork).toHaveBeenCalledExactlyOnceWith();
    expect(screen.getByLabelText("Brief 候選主張")).toHaveValue("Keep the current draft.");
    expect(screen.getByLabelText("Brief 引文起始行")).toHaveValue(2);
    expect(screen.getByLabelText("Brief 引文結束行")).toHaveValue(2);
    expect(bridge.PrepareBriefCandidate).not.toHaveBeenCalled();
  });
  it("does not revive an unsent decision draft after changing a launcher and changing it back", async () => {
    const state = briefDraftState(true);
    const { bridge } = await renderReady(state);
    fireEvent.click(screen.getByRole("radio", { name: /^reject ·/ }));
    fireEvent.change(screen.getByLabelText("Brief 本次具體理由"), { target: { value: "Unsent reason before launcher changes." } });
    fireEvent.click(screen.getByRole("button", { name: "資料源與連線", exact: true }));
    for (const queryLauncher of ["/other/query", state.settings.queryLauncher]) {
      fireEvent.change(screen.getByLabelText("Query launcher"), { target: { value: queryLauncher } });
      bridge.SaveSettings.mockImplementationOnce(async (settings) => ({ ...state, settings }));
      fireEvent.click(screen.getByRole("button", { name: "套用本次設定" }));
      await waitFor(() => expect(screen.getByRole("button", { name: "套用本次設定" })).toBeDisabled());
      await waitFor(() => expect(screen.getByRole("button", { name: "新工作" })).toBeEnabled());
    }
    fireEvent.click(screen.getByRole("button", { name: "工作台", exact: true }));
    expect(screen.getByLabelText("Brief 本次具體理由")).toHaveValue("");
    for (const radio of screen.getAllByRole("radio")) expect(radio).not.toBeChecked();
    expect(bridge.SaveSettings).toHaveBeenCalledTimes(2);
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
  });
  it("clears the old Brief draft when a failed open response is recovered by a fresh completed snapshot", async () => {
    const state = briefDraftState();
    const { bridge } = await renderReady(state);
    fireEvent.change(screen.getByLabelText("Brief 候選主張"), { target: { value: "Only for the previously opened work." } });
    fireEvent.change(screen.getByLabelText("Brief 引文起始行"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Brief 引文結束行"), { target: { value: "2" } });
    const recovered = structuredClone(state);
    recovered.brief.path = "/private/recovered-open.json";
    recovered.brief.digest = "recovered-open-digest";
    recovered.events.push({ id: "recovered-completed-open", action: "brief_open", status: "completed", detail: "Synthetic lost response", time: "" });
    bridge.ChooseBriefWork.mockRejectedValueOnce(new Error("Synthetic open response lost"));
    bridge.Snapshot.mockResolvedValueOnce(recovered);
    fireEvent.click(screen.getByRole("button", { name: "恢復 Brief 工作" }));
    await waitFor(() => expect(screen.getByText(recovered.brief.path)).toBeInTheDocument());
    expect(screen.getByLabelText("Brief 候選主張")).toHaveValue(recovered.brief.report.text);
    expect(screen.getByLabelText("Brief 引文起始行")).toHaveValue(null);
    expect(screen.getByLabelText("Brief 引文結束行")).toHaveValue(null);
    expect(bridge.ChooseBriefWork).toHaveBeenCalledExactlyOnceWith();
    expect(bridge.PrepareBriefCandidate).not.toHaveBeenCalled();
  });
  it.each(["model", "sourceID", "normalized-same-settings"])("keeps an unsent decision draft when saving %s does not change review launchers or mode", async (change) => {
    const state = briefDraftState(true);
    const { bridge } = await renderReady(state);
    fireEvent.click(screen.getByRole("radio", { name: /^reject ·/ }));
    fireEvent.change(screen.getByLabelText("Brief 本次具體理由"), { target: { value: "Do not discard this exact unsent reason." } });
    fireEvent.click(screen.getByRole("button", { name: "資料源與連線", exact: true }));
    const label = change === "model" ? "模型名稱" : change === "sourceID" ? "來源識別 ID" : "本機模型 Base URL";
    const value = change === "normalized-same-settings" ? "http://localhost:11434/v1" : "different-next-input";
    fireEvent.change(screen.getByLabelText(label), { target: { value } });
    bridge.SaveSettings.mockImplementationOnce(async (settings) => ({ ...state, settings: change === "normalized-same-settings" ? state.settings : settings }));
    fireEvent.click(screen.getByRole("button", { name: "套用本次設定" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "新工作" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "工作台", exact: true }));
    expect(screen.getByRole("radio", { name: /^reject ·/ })).toBeChecked();
    expect(screen.getByLabelText("Brief 本次具體理由")).toHaveValue("Do not discard this exact unsent reason.");
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
  });
  it("keeps Brief drafts only in the current App session without browser storage", async () => {
    const storageGet = vi.spyOn(Storage.prototype, "getItem");
    const storageSet = vi.spyOn(Storage.prototype, "setItem");
    try {
      const state = briefDraftState();
      const { bridge } = installBridge(state);
      const mounted = render(<App />);
      await waitFor(() => expect(screen.getByLabelText("Brief 候選主張")).toBeEnabled());
      fireEvent.change(screen.getByLabelText("Brief 候選主張"), { target: { value: "Session-only draft." } });
      mounted.unmount();
      render(<App />);
      await waitFor(() => expect(screen.getByLabelText("Brief 候選主張")).toBeEnabled());
      expect(screen.getByLabelText("Brief 候選主張")).toHaveValue(state.brief.report.text);
      expect(storageGet).not.toHaveBeenCalled();
      expect(storageSet).not.toHaveBeenCalled();
      expect(bridge.PrepareBriefCandidate).not.toHaveBeenCalled();
    } finally {
      storageGet.mockRestore();
      storageSet.mockRestore();
    }
  });
  it.each(["ChooseBriefSource", "ChooseBriefWork"])("opens the dedicated Brief workflow through %s without automatic extraction", async (method) => {
    const initial = emptyState();
    const installed = await renderReady(initial);
    const next = structuredClone(initial);
    next.brief = {
      model: "", path: "/private/brief-work.json", outcome: "inspected", failureStage: "",
      report: { body_sha256: "source-digest", prompt_version: "detective-brief-prompt/v2", stage: "inspected", text: "",
        source: { source_id: "source-test", source_revision: "frozen-1", source_url: "https://example.test/status", observed_at: "2026-09-11T00:00:00Z", coverage: "full_document", limitations: [], body: "Complete original source." } },
    };
    installed.bridge[method].mockResolvedValueOnce(next);
    fireEvent.click(screen.getByRole("button", { name: method === "ChooseBriefSource" ? "開啟 Brief 來源" : "恢復 Brief 工作" }));
    await waitFor(() => expect(screen.getByLabelText("Brief 完整來源原文")).toHaveTextContent("Complete original source."));
    expect(installed.bridge[method]).toHaveBeenCalledOnce();
    for (const name of ["ExtractBrief", "PrepareBriefCandidate", "SubmitBriefPending", "PrepareBriefReview", "ApplyBriefReview", "SendMessage"]) {
      expect(installed.bridge[name]).not.toHaveBeenCalled();
    }
    expect(screen.getByRole("button", { name: "以本機模型產生 Brief" })).toBeDisabled();
  });
  it("restores the frozen Brief statement and citation across page changes and reopening", async () => {
    const saved = emptyState();
    saved.brief = {
      model: "test-local-model", path: "/private/saved-brief-work.json", digest: "saved-work-digest", outcome: "candidate_saved", failureStage: "",
      report: { body_sha256: "source-digest", prompt_version: "detective-brief-prompt/v2", stage: "complete", text: "Original model summary.",
        source: { source_id: "source-test", source_revision: "frozen-1", source_url: "https://example.test/status", observed_at: "2026-09-11T00:00:00Z", coverage: "full_document", limitations: [], body: "Navigation.\r\nAtlas requests failed in the north region.\r\nOther regions were unaffected.\r\n" } },
      submission: { digest: "saved-submission-digest", statement: "A separately edited candidate statement.",
        citation: { start_line: 2, end_line: 3, exact_quote: "Atlas requests failed in the north region.\nOther regions were unaffected." } },
    };
    const original = JSON.stringify(saved);
    const { bridge } = installBridge();
    const first = render(<App />);
    await waitFor(() => expect(screen.getByRole("button", { name: "恢復 Brief 工作" })).toBeEnabled());
    bridge.ChooseBriefWork.mockResolvedValueOnce(saved);
    fireEvent.click(screen.getByRole("button", { name: "恢復 Brief 工作" }));
    await screen.findByLabelText("Brief 完整候選綁定");
    const expectSavedSelection = () => {
      expect(screen.getByLabelText("Brief 引文起始行")).toHaveValue(2);
      expect(screen.getByLabelText("Brief 引文結束行")).toHaveValue(3);
      expect(screen.getByLabelText("Brief 候選主張")).toHaveValue(saved.brief.submission.statement);
      for (const label of ["Brief 候選主張", "Brief 引文起始行", "Brief 引文結束行"]) expect(screen.getByLabelText(label)).toBeDisabled();
      expect(screen.getByLabelText("Brief 選定精確引文").textContent).toBe(saved.brief.submission.citation.exact_quote);
      expect(screen.getByLabelText("Brief 完整來源原文").textContent).toBe(saved.brief.report.source.body);
      expect(screen.getByLabelText("模型 Brief 原文").textContent).toBe(saved.brief.report.text);
      expect(screen.getByLabelText("Brief 完整候選綁定").textContent).toBe(JSON.stringify(saved.brief.submission, null, 2));
    };
    expectSavedSelection();
    for (const page of ["資料源與連線", "執行紀錄"]) {
      fireEvent.click(screen.getByRole("button", { name: page, exact: true }));
      expect(screen.queryByLabelText("Brief 選定精確引文")).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: "工作台", exact: true }));
      expectSavedSelection();
    }
    first.unmount();
    bridge.Snapshot.mockResolvedValueOnce(saved);
    render(<App />);
    await screen.findByLabelText("Brief 完整候選綁定");
    expectSavedSelection();
    expect(bridge.Snapshot).toHaveBeenCalledTimes(2);
    expect(bridge.ChooseBriefWork).toHaveBeenCalledExactlyOnceWith();
    for (const [name, method] of Object.entries(bridge)) {
      if (!["Snapshot", "ChooseBriefWork"].includes(name)) expect(method).not.toHaveBeenCalled();
    }
    expect(JSON.stringify(saved)).toBe(original);
  });
  it("keeps exact saved Brief decision retry after a pending query without automatically writing", async () => {
    const saved = emptyState();
    saved.settings = { ...saved.settings, mode: "local", intakeLauncher: "/test/intake", queryLauncher: "/test/query", reviewLauncher: "/test/review" };
    saved.brief = {
      model: "test-local-model", path: "/private/saved-review-work.json", digest: "saved-review-digest", outcome: "decision_saved", failureStage: "review_write",
      report: { body_sha256: "source-digest", prompt_version: "detective-brief-prompt/v2", stage: "complete", text: "Atlas has recovered.",
        source: { source_id: "atlas-test", source_revision: "frozen-1", source_url: "https://example.test/status", observed_at: "2026-09-11T00:00:00Z", coverage: "full_document", limitations: [], body: "Atlas requests failed.\nNo recovery has been reported." } },
      submission: { digest: "submission-digest", statement: "Atlas has recovered.",
        citation: { start_line: 1, end_line: 2, exact_quote: "Atlas requests failed.\nNo recovery has been reported." } },
      handoff: { proposal_occurrence_id: "occ:exact-current" },
      review: {
        display: { review_display_artifact_id: "display:exact-current", payload_utf8: '{"statement":"Atlas has recovered.","source":"No recovery has been reported."}' },
        subject: { review_subject: { proposal_occurrence_id: "occ:exact-current", proposal_basis_id: "basis:exact-current" } },
      },
      decision: { displayID: "display:exact-current", outcome: "reject", reason: "模擬審查：來源未報告恢復，不足以支持候選。", reviewLauncher: "/test/review", queryLauncher: "/test/query" },
    };
    const original = JSON.stringify(saved);
    const queried = structuredClone(saved);
    queried.brief.outcome = "pending";
    queried.brief.failureStage = "";
    const terminal = structuredClone(queried);
    terminal.brief.outcome = "reject_verified";
    const { bridge } = await renderReady(emptyState());
    bridge.ChooseBriefWork.mockResolvedValueOnce(saved);
    bridge.QueryBriefPending.mockResolvedValueOnce(queried);
    bridge.ApplyBriefReview.mockResolvedValueOnce(terminal);
    fireEvent.click(screen.getByRole("button", { name: "恢復 Brief 工作" }));
    const retryName = "核對並重試已保存決定";
    await waitFor(() => expect(screen.getByRole("button", { name: retryName })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "查回這筆 Brief 狀態" }));
    await waitFor(() => expect(screen.getByText("無已記錄的失敗")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: retryName })).toBeEnabled();
    for (const page of ["資料源與連線", "執行紀錄"]) {
      fireEvent.click(screen.getByRole("button", { name: page, exact: true }));
      expect(screen.queryByRole("button", { name: retryName })).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: "工作台", exact: true }));
      expect(screen.getByRole("button", { name: retryName })).toBeEnabled();
    }
    expect(screen.getByLabelText("Brief 已保存決定").textContent).toBe(JSON.stringify(saved.brief.decision, null, 2));
    expect(screen.getByLabelText("Brief 本次具體理由")).toBeDisabled();
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: retryName }));
    let modal = within(screen.getByRole("dialog", { name: "核對同一份已保存審查決定" }));
    fireEvent.click(modal.getByRole("button", { name: "返回檢查" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: retryName }));
    modal = within(screen.getByRole("dialog", { name: "核對同一份已保存審查決定" }));
    expect(modal.getByLabelText("待確認完整 MCP 審查內容").textContent).toBe(saved.brief.review.display.payload_utf8);
    expect(modal.getByLabelText("待確認精確審查 subject").textContent).toBe(JSON.stringify(saved.brief.review.subject, null, 2));
    expect(modal.getByLabelText("待確認原始理由").textContent).toBe(saved.brief.decision.reason);
    for (const launcher of [saved.settings.intakeLauncher, saved.settings.queryLauncher, saved.settings.reviewLauncher]) {
      expect(modal.getByText(launcher)).toBeInTheDocument();
    }
    expect(modal.getByText(saved.brief.decision.displayID)).toBeInTheDocument();
    expect(modal.getByText("reject")).toBeInTheDocument();
    const confirm = modal.getByRole("button", { name: "送出本次精確審查決定" });
    expect(confirm).toBeDisabled();
    fireEvent.click(confirm);
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
    fireEvent.click(modal.getByRole("checkbox"));
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);
    await waitFor(() => expect(bridge.ApplyBriefReview).toHaveBeenCalledExactlyOnceWith({
      displayID: saved.brief.decision.displayID, outcome: "reject", reason: saved.brief.decision.reason,
    }));
    await waitFor(() => expect(screen.queryByRole("button", { name: retryName })).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "取得這筆精確審查內容" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "檢查本次審查決定" })).toBeDisabled();
    for (const radio of screen.getAllByRole("radio")) expect(radio).toBeDisabled();
    expect(screen.getByLabelText("Brief 已保存決定").textContent).toBe(JSON.stringify(saved.brief.decision, null, 2));
    expect(bridge.ChooseBriefWork).toHaveBeenCalledExactlyOnceWith();
    expect(bridge.QueryBriefPending).toHaveBeenCalledExactlyOnceWith();
    for (const [name, method] of Object.entries(bridge)) {
      if (!["Snapshot", "ChooseBriefWork", "QueryBriefPending", "ApplyBriefReview"].includes(name)) expect(method).not.toHaveBeenCalled();
    }
    expect(JSON.stringify(saved)).toBe(original);
    expect(queried.brief.decision).toEqual(saved.brief.decision);
  });
  it("recovers a missing Brief disposition receipt only through exact reconfirmation after a rejected query", async () => {
    const saved = emptyState();
    saved.settings = { ...saved.settings, mode: "local", intakeLauncher: "/test/intake", queryLauncher: "/test/query", reviewLauncher: "/test/review" };
    saved.brief = {
      model: "test-local-model", path: "/private/missing-ack-work.json", digest: "missing-ack-digest", outcome: "decision_saved", failureStage: "review_write",
      report: { body_sha256: "source-digest", prompt_version: "detective-brief-prompt/v2", stage: "complete", text: "Atlas has recovered.",
        source: { source_id: "atlas-test", source_revision: "frozen-1", source_url: "https://example.test/status", observed_at: "2026-09-11T00:00:00Z", coverage: "full_document", limitations: [], body: "Atlas requests failed.\nNo recovery has been reported." } },
      submission: { digest: "submission-digest", statement: "Atlas has recovered.",
        citation: { start_line: 1, end_line: 2, exact_quote: "Atlas requests failed.\nNo recovery has been reported." } },
      handoff: { proposal_occurrence_id: "occ:exact-current" },
      review: {
        display: { review_display_artifact_id: "display:exact-current", payload_utf8: '{"statement":"Atlas has recovered.","source":"No recovery has been reported."}' },
        subject: { review_subject: { proposal_occurrence_id: "occ:exact-current", proposal_basis_id: "basis:exact-current" } },
      },
      decision: { displayID: "display:exact-current", outcome: "reject", reason: "模擬審查：來源未報告恢復，不足以支持候選。", reviewLauncher: "/test/review", queryLauncher: "/test/query" },
    };
    const original = JSON.stringify(saved);
    const queried = structuredClone(saved);
    queried.brief.outcome = "rejected";
    queried.brief.failureStage = "";
    const verified = structuredClone(queried);
    verified.brief.outcome = "reject_verified";
    verified.brief.disposition = {
      proposal_occurrence_id: saved.brief.handoff.proposal_occurrence_id,
      admission_decision_id: "adm:original-reject", admission_outcome: "rejected",
      decision_by: "mock:original-reviewer", decision_reason: saved.brief.decision.reason, replayed: true,
    };
    const { bridge } = await renderReady(emptyState());
    bridge.ChooseBriefWork.mockResolvedValueOnce(saved);
    bridge.QueryBriefPending.mockResolvedValueOnce(queried);
    bridge.ApplyBriefReview.mockResolvedValueOnce(verified);
    fireEvent.click(screen.getByRole("button", { name: "恢復 Brief 工作" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "查回這筆 Brief 狀態" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "查回這筆 Brief 狀態" }));
    await waitFor(() => expect(screen.getByText("無已記錄的失敗")).toBeInTheDocument());
    const recoveryName = "核對原決定以取回收據";
    expect(screen.getByRole("button", { name: recoveryName })).toBeEnabled();
    for (const page of ["資料源與連線", "執行紀錄"]) {
      fireEvent.click(screen.getByRole("button", { name: page, exact: true }));
      expect(screen.queryByRole("button", { name: recoveryName })).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: "工作台", exact: true }));
      expect(screen.getByRole("button", { name: recoveryName })).toBeEnabled();
    }
    expect(screen.getByLabelText("Brief 已保存決定").textContent).toBe(JSON.stringify(saved.brief.decision, null, 2));
    expect(screen.getByLabelText("Brief 本次具體理由")).toBeDisabled();
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: recoveryName }));
    let modal = within(screen.getByRole("dialog", { name: "核對同一份已保存審查決定" }));
    fireEvent.click(modal.getByRole("button", { name: "返回檢查" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: recoveryName }));
    modal = within(screen.getByRole("dialog", { name: "核對同一份已保存審查決定" }));
    expect(modal.getByLabelText("待確認完整 MCP 審查內容").textContent).toBe(saved.brief.review.display.payload_utf8);
    expect(modal.getByLabelText("待確認精確審查 subject").textContent).toBe(JSON.stringify(saved.brief.review.subject, null, 2));
    expect(modal.getByLabelText("待確認原始理由").textContent).toBe(saved.brief.decision.reason);
    for (const launcher of [saved.settings.intakeLauncher, saved.settings.queryLauncher, saved.settings.reviewLauncher]) expect(modal.getByText(launcher)).toBeInTheDocument();
    expect(modal.getByText(saved.brief.decision.displayID)).toBeInTheDocument();
    expect(modal.getByText("reject")).toBeInTheDocument();
    const confirm = modal.getByRole("button", { name: "送出本次精確審查決定" });
    expect(confirm).toBeDisabled();
    fireEvent.click(confirm);
    expect(bridge.ApplyBriefReview).not.toHaveBeenCalled();
    fireEvent.click(modal.getByRole("checkbox"));
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);
    await waitFor(() => expect(bridge.ApplyBriefReview).toHaveBeenCalledExactlyOnceWith({
      displayID: saved.brief.decision.displayID, outcome: "reject", reason: saved.brief.decision.reason,
    }));
    await waitFor(() => expect(screen.queryByRole("button", { name: recoveryName })).not.toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "核對並重試已保存決定" })).not.toBeInTheDocument();
    const receipt = screen.getByText("完整 Brief 工作與收據").closest("details");
    expect(receipt.querySelector("pre").textContent).toBe(JSON.stringify(verified.brief, null, 2));
    expect(screen.getByRole("button", { name: "取得這筆精確審查內容" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "檢查本次審查決定" })).toBeDisabled();
    for (const radio of screen.getAllByRole("radio")) expect(radio).toBeDisabled();
    expect(bridge.ChooseBriefWork).toHaveBeenCalledExactlyOnceWith();
    expect(bridge.QueryBriefPending).toHaveBeenCalledExactlyOnceWith();
    for (const [name, method] of Object.entries(bridge)) {
      if (!["Snapshot", "ChooseBriefWork", "QueryBriefPending", "ApplyBriefReview"].includes(name)) expect(method).not.toHaveBeenCalled();
    }
    expect(JSON.stringify(saved)).toBe(original);
    expect(queried.brief.decision).toEqual(saved.brief.decision);
  });
  it("does not claim success without a native bridge", () => {
    render(<App />);
    expect(screen.getByText("需從桌面程式啟動")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /試用離線示範/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: "送出訊息" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "檢查並送 pending" }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "開啟來源收據" })).toBeDisabled();
  });
  it("opens a saved receipt offline and shows complete text-only capture data without granting authority", async () => {
    const state = emptyState();
    const { bridge } = await renderReady(state);
    const reopened = inspectedReceiptState();
    bridge.ChooseSourceReceipt.mockResolvedValueOnce(reopened);
    fireEvent.click(screen.getByRole("button", { name: "資料源與連線" }));
    const open = screen.getByRole("button", { name: "開啟來源收據" });
    expect(open).toBeEnabled();
    fireEvent.click(open);
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: /來源原文/ })).toHaveAttribute(
        "aria-selected",
        "true",
      ),
    );
    expect(bridge.ChooseSourceReceipt).toHaveBeenCalledExactlyOnceWith();
    expect(screen.getByLabelText("完整來源原文").textContent).toBe(
      reopened.source.rawText,
    );
    const card = screen.getByLabelText("已保存來源收據檢視");
    expect(card).not.toHaveAttribute("open");
    fireEvent.click(within(card).getByText("已保存的來源收據與離線核對"));
    const capture = reopened.source.capture;
    const inspection = capture.inspection;
    expect(within(card).getByText("檔案與已保存收據一致")).toBeInTheDocument();
    expect(
      within(card).getByText(
        /不證明來源真實性、人工核准、最新 DB 狀態或 canonical 採納/,
      ),
    ).toBeInTheDocument();
    for (const value of [
      capture.capturedAt,
      capture.revision,
      inspection.verifiedAt,
      capture.receipt.path,
      capture.receipt.sha256,
      capture.rawResult.path,
      capture.rawResult.sha256,
    ]) {
      expect(within(card).getByText(value)).toBeInTheDocument();
    }
    expect(within(card).getByLabelText("完整保存連線設定").textContent).toBe(
      JSON.stringify(inspection.config, null, 2),
    );
    expect(within(card).getByLabelText("完整保存工具契約").textContent).toBe(
      JSON.stringify(inspection.tool, null, 2),
    );
    expect(within(card).getByLabelText("已保存精確工具參數").textContent).toBe(
      inspection.argumentsJSON,
    );
    fireEvent.click(within(card).getByText("完整原始工具結果 JSON"));
    expect(within(card).getByLabelText("已保存原始工具結果").textContent).toBe(
      inspection.rawResultJSON,
    );
    expect(card.querySelector("script, img, iframe, svg, a")).toBeNull();
    for (const name of [
      "SaveSettings",
      "LoadDemo",
      "ChooseSource",
      "ChooseBatch",
      "SendMessage",
      "Extract",
      "SubmitPending",
      "QueryPending",
      "DiscoverTools",
      "CallSourceTool",
    ]) {
      expect(bridge[name]).not.toHaveBeenCalled();
    }
    fireEvent.click(screen.getByRole("button", { name: "資料源與連線" }));
    expect(screen.getByText("已生效：離線演練")).toBeInTheDocument();
    expect(
      screen.queryByDisplayValue("/historical/launcher"),
    ).not.toBeInTheDocument();
  });

  it.each(["batchPath", "batchDigest"])(
    "blocks receipt reopening for an active %s and explains why",
    async (field) => {
      const state = emptyState();
      state[field] = "active-batch";
      const { bridge } = await renderReady(state);
      const open = screen.getByRole("button", { name: "開啟來源收據" });
      expect(open).toBeDisabled();
      expect(
        document.getElementById(open.getAttribute("aria-describedby"))
          .textContent,
      ).toMatch(/目前有作用中的批次.*新工作/);
      fireEvent.click(open);
      expect(bridge.ChooseSourceReceipt).not.toHaveBeenCalled();
    },
  );

  it("disables the receipt chooser while working and leaves cancellation available", async () => {
    const { bridge } = await renderReady(emptyState());
    let finish;
    bridge.ChooseSourceReceipt.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    fireEvent.click(screen.getByRole("button", { name: "開啟來源收據" }));
    expect(screen.getByRole("button", { name: "開啟來源收據" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "取消操作" })).toBeEnabled();
    await act(async () => {
      finish(emptyState());
    });
    expect(bridge.ChooseSourceReceipt).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("檔案與已保存收據一致")).not.toBeInTheDocument();
  });

  it.each(["cancelled", "rejected", "stale_completion"])(
    "preserves an older source after %s receipt opening without claiming a new inspection",
    async (outcome) => {
      const state = inspectedReceiptState();
      const { bridge, update } = await renderReady(state);
      fireEvent.click(screen.getByRole("tab", { name: /完整候選/ }));
      const result = structuredClone(state);
      if (outcome === "cancelled")
        result.events.push({
          id: "receipt-cancel",
          action: "開啟來源收據",
          status: "cancelled",
          detail: "已取消",
          time: "",
        });
      if (outcome === "rejected") {
        bridge.ChooseSourceReceipt.mockImplementationOnce(async () => {
          update(result);
          throw new Error(
            "private file body and credential details must not leak",
          );
        });
      } else bridge.ChooseSourceReceipt.mockResolvedValueOnce(result);
      fireEvent.click(screen.getByRole("button", { name: "開啟來源收據" }));
      await waitFor(() =>
        expect(bridge.ChooseSourceReceipt).toHaveBeenCalledTimes(1),
      );
      await waitFor(() =>
        expect(
          screen.getByRole("button", { name: "開啟來源收據" }),
        ).toBeEnabled(),
      );
      expect(screen.getByRole("tab", { name: /完整候選/ })).toHaveAttribute(
        "aria-selected",
        "true",
      );
      if (outcome === "rejected") {
        expect(
          screen.getByText(/操作未完成。已重新同步桌面狀態/),
        ).toBeInTheDocument();
        expect(bridge.Snapshot.mock.calls.length).toBeGreaterThan(1);
      }
      expect(screen.queryByText(/private file body/)).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole("tab", { name: /來源原文/ }));
      expect(screen.getByLabelText("完整來源原文").textContent).toBe(
        state.source.rawText,
      );
      expect(
        screen.getByText(state.source.capture.inspection.verifiedAt),
      ).toBeInTheDocument();
      expect(bridge.CallSourceTool).not.toHaveBeenCalled();
      expect(bridge.SubmitPending).not.toHaveBeenCalled();
    },
  );

  it("does not enable receipt opening when an older native bridge lacks the method", async () => {
    const { bridge } = installBridge(emptyState());
    delete bridge.ChooseSourceReceipt;
    render(<App />);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "新工作" })).toBeEnabled(),
    );
    const open = screen.getByRole("button", { name: "開啟來源收據" });
    expect(open).toBeDisabled();
    expect(
      document.getElementById(open.getAttribute("aria-describedby"))
        .textContent,
    ).toMatch(/桌面版本/);
  });
  it("recognizes a fresh successful inspection of the same receipt path", async () => {
    const state = inspectedReceiptState();
    const { bridge } = await renderReady(state);
    const reopened = structuredClone(state);
    reopened.source.capture.inspection.verifiedAt = "2026-09-09T04:00:00Z";
    reopened.events.push({
      ...state.events[0],
      id: "receipt-inspected-2",
      time: "2026-09-09T04:00:00Z",
    });
    bridge.ChooseSourceReceipt.mockResolvedValueOnce(reopened);
    fireEvent.click(screen.getByRole("tab", { name: /完整候選/ }));
    fireEvent.click(screen.getByRole("button", { name: "開啟來源收據" }));
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: /來源原文/ })).toHaveAttribute(
        "aria-selected",
        "true",
      ),
    );
    expect(
      screen.getByText(reopened.source.capture.inspection.verifiedAt),
    ).toBeInTheDocument();
    expect(bridge.ChooseSourceReceipt).toHaveBeenCalledTimes(1);
    expect(bridge.CallSourceTool).not.toHaveBeenCalled();
  });
  it("does not claim an offline recheck for a live capture without inspection metadata", async () => {
    const state = inspectedReceiptState();
    delete state.source.capture.inspection;
    state.events = [];
    await renderReady(state);
    const card = screen.getByLabelText("已保存來源收據檢視");
    expect(
      within(card).getByText("這份來源尚無離線重新核對紀錄。"),
    ).toBeInTheDocument();
    expect(
      within(card).queryByText("檔案與已保存收據一致"),
    ).not.toBeInTheDocument();
    expect(
      within(card).queryByLabelText("已保存精確工具參數"),
    ).not.toBeInTheDocument();
  });
  it("keeps mutations disabled until the initial native snapshot arrives", async () => {
    const { bridge } = installBridge();
    let finish;
    bridge.Snapshot.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    render(<App />);
    expect(screen.getByRole("button", { name: "新工作" })).toBeDisabled();
    expect(screen.getByText(/正在取得桌面工作狀態/)).toBeInTheDocument();
    await act(async () => {
      finish(emptyState());
    });
    expect(screen.getByRole("button", { name: "新工作" })).toBeEnabled();
    expect(bridge.LoadDemo).not.toHaveBeenCalled();
  });
  it("routes chat admit only to SendMessage, never to a tool or writer", async () => {
    const { bridge } = await renderReady(emptyState());
    expect(screen.queryByLabelText("聊天模型輸入範圍")).not.toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent("離線固定操作演練，不是人工審核");
    expect(screen.getByRole("note")).toHaveTextContent("載入合成來源後也是如此");
    fireEvent.change(screen.getByLabelText("對話訊息"), {
      target: { value: "admit" },
    });
    fireEvent.click(screen.getByRole("button", { name: "送出訊息" }));
    await waitFor(() =>
      expect(bridge.SendMessage).toHaveBeenCalledWith("admit"),
    );
    expect(bridge.SubmitPending).not.toHaveBeenCalled();
    expect(bridge.CallSourceTool).not.toHaveBeenCalled();
  });
  it("keeps the offline-review notice after a synthetic source is loaded", async () => {
    const state = readyState();
    state.settings.mode = "demo";
    state.source.kind = "demo";
    await renderReady(state);
    expect(screen.getByRole("note")).toHaveTextContent("不會採納");
    expect(screen.getByLabelText("完整來源原文")).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/只回固定提示，不會採納/)).toBeInTheDocument();
    expect(screen.queryByLabelText("聊天模型輸入範圍")).not.toBeInTheDocument();
    expect(screen.getByText("固定提示 · 未採納")).toBeInTheDocument();
  });
  it("shows local chat input boundaries beside the composer without implying automatic redaction or tool authority", async () => {
    const { bridge } = await renderReady(readyState());
    const notice = screen.getByRole("note", { name: "聊天模型輸入範圍" });
    expect(notice.closest("form")).toContainElement(screen.getByLabelText("對話訊息"));
    expect(notice).toHaveTextContent("對話文字、來源原文，以及完整候選、引用與限制");
    expect(notice).toHaveTextContent("不附帶操作路徑、設定、收據或 DB 查詢結果");
    expect(notice).toHaveTextContent("這不是敏感內容自動遮蔽");
    expect(notice).toHaveTextContent("寫進對話、來源或候選的敏感資訊仍會送入模型");
    expect(notice).toHaveTextContent("聊天模型沒有工具呼叫或寫入權限");
    expect(bridge.SendMessage).not.toHaveBeenCalled();
    expect(bridge.CallSourceTool).not.toHaveBeenCalled();
    expect(bridge.SubmitPending).not.toHaveBeenCalled();
  });
  it("shows a new saved capture even when its content identity matches the previous source", async () => {
    const state = readyState(0);
    state.batchPath = "";
    state.batchDigest = "";
    const { bridge, update } = await renderReady(state);
    bridge.CallSourceTool.mockImplementationOnce(async () => {
      const result = capturedSourceState();
      update(result);
      return result;
    });
    await approveSourceTool();
    await waitFor(() => expect(screen.getByText("來源工具已完成")).toBeInTheDocument());
    expect(screen.getByText(/尚未抽取、送入 pending 或採納/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "資料源與連線", level: 1 })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "查看本次來源" }));
    expect(screen.getByRole("tab", { name: /來源原文/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByLabelText("完整來源原文")).toBeInTheDocument();
    expect(bridge.CallSourceTool).toHaveBeenCalledTimes(1);
    expect(bridge.SubmitPending).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "查看執行紀錄" }));
    expect(screen.getByRole("heading", { name: "執行紀錄", level: 1 })).toBeInTheDocument();
  });
  it.each(["resolved-error", "failed-event", "busy", "retained", "old-event"])(
    "does not present %s source results as new successful capture",
    async (scenario) => {
      const state = readyState(0);
      if (scenario === "old-event") {
        state.events = capturedSourceState().events;
      }
      const { bridge, update } = await renderReady(state);
      bridge.CallSourceTool.mockImplementationOnce(async () => {
        const result = capturedSourceState();
        if (scenario === "resolved-error") {
          result.error = "取消期間可能已保存部分檔案";
        }
        if (scenario === "failed-event") result.events[0].status = "failed";
        if (scenario === "busy") result.busy = true;
        if (scenario === "retained") result.source.path = state.source.path;
        update(result);
        return result;
      });
      await approveSourceTool();
      await waitFor(() => expect(screen.getByText("來源工具未確認完成")).toBeInTheDocument());
      expect(screen.queryByText("來源工具已完成")).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "查看本次來源" })).not.toBeInTheDocument();
      expect(bridge.CallSourceTool).toHaveBeenCalledTimes(1);
      expect(bridge.SubmitPending).not.toHaveBeenCalled();
    },
  );
  it("offers execution history after a source rejection while preserving the earlier source", async () => {
    const state = readyState(0);
    const { bridge, update } = await renderReady(state);
    bridge.CallSourceTool.mockImplementationOnce(async () => {
      update({ ...state, error: "來源工作已取消；先前來源仍保留。" });
      throw new Error("raw upstream detail must stay private");
    });
    await approveSourceTool();
    await waitFor(() => expect(screen.getByText("來源工作已取消；先前來源仍保留。")).toBeInTheDocument());
    expect(screen.queryByText("來源工具已完成")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "查看本次來源" })).not.toBeInTheDocument();
    expect(screen.queryByText(/raw upstream detail/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "查看執行紀錄" }));
    expect(screen.getByRole("heading", { name: "執行紀錄", level: 1 })).toBeInTheDocument();
    expect(bridge.CallSourceTool).toHaveBeenCalledTimes(1);
  });
  it("clears a previous source-success notice when the next operation fails", async () => {
    const state = readyState(0);
    const { bridge, update } = await renderReady(state);
    bridge.CallSourceTool.mockImplementationOnce(async () => {
      const result = capturedSourceState();
      update(result);
      return result;
    });
    await approveSourceTool();
    await waitFor(() => expect(screen.getByText("來源工具已完成")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "查看本次來源" }));
    bridge.SendMessage.mockRejectedValueOnce(new Error("private model failure"));
    fireEvent.change(screen.getByLabelText("對話訊息"), { target: { value: "read this" } });
    fireEvent.click(screen.getByRole("button", { name: "送出訊息" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("操作未完成"));
    expect(screen.queryByText("來源工具已完成")).not.toBeInTheDocument();
    expect(screen.queryByText(/private model failure/)).not.toBeInTheDocument();
    expect(bridge.CallSourceTool).toHaveBeenCalledTimes(1);
  });
  it("shows every candidate field, source quote and partial failure instead of only the first candidate", async () => {
    const state = readyState(16);
    state.extraction = {
      extractor: {
        name: "lab-status-extractor",
        version: "0.1.2",
        model: "test-only",
      },
      source: { sha256: "source-hash" },
      section: { heading: "Status" },
      summary: { attempted: 1, validated: 1, failed: 0, not_attempted: 0 },
      rows: [
        {
          row: { start_line: 4, end_line: 4 },
          status: "validated",
          result: {
            limitations: ["完整限制"],
            abstentions: [{ subject: "未知項目", reason: "未提供" }],
            abstention_reason: "沒有額外依據",
          },
        },
      ],
    };
    await renderReady(state);
    fireEvent.click(screen.getByRole("tab", { name: /完整候選/ }));
    expect(screen.getAllByTestId(/^candidate-/)).toHaveLength(16);
    const last = within(screen.getByTestId("candidate-16"));
    for (const text of [
      "完整敘述 16",
      "不能認定部署 16",
      "限定條件 16",
      "受阻 16",
      "精確原文 16",
      "query_readback",
      "runtime_core",
      "selected",
    ])
      expect(last.getByText(text)).toBeInTheDocument();
    expect(screen.getByText("完整限制")).toBeInTheDocument();
    expect(screen.getByText("沒有額外依據")).toBeInTheDocument();
    expect(
      screen.getByText("完整 RowBatch（包含來源與未完成列）"),
    ).toBeInTheDocument();
  });
  it("shows validated abstention with no candidates after extracting the exact row without enabling pending", async () => {
    const state = capturedSourceState();
    const { bridge, update } = await renderReady(state);
    const result = {
      ...state,
      extraction: {
        extractor: { name: "lab-status-extractor", version: "0.1.2", model: "test-only" },
        source: { sha256: state.source.sha256 },
        section: { heading: "Status at a Glance" },
        summary: { attempted: 1, validated: 1, failed: 0, not_attempted: 0 },
        rows: [{
          row: { start_line: 4, end_line: 4 },
          status: "validated",
          result: {
            outcome: "abstained",
            records: [],
            limitations: ["僅檢視本次選定資料列"],
            abstentions: [],
            abstention_reason: "來源資訊不足，無法建立有依據的候選",
          },
        }],
      },
    };
    bridge.Extract.mockImplementationOnce(async () => {
      update(result);
      return result;
    });
    fireEvent.change(screen.getByLabelText("選取來源資料列"), {
      target: { value: "4" },
    });
    fireEvent.click(screen.getByRole("button", { name: /抽取此列候選/ }));
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: /完整候選 0/ })).toHaveAttribute("aria-selected", "true"),
    );
    expect(bridge.Extract).toHaveBeenCalledExactlyOnceWith(4);
    expect(screen.queryAllByTestId(/^candidate-/)).toHaveLength(0);
    expect(screen.getByText("僅檢視本次選定資料列")).toBeVisible();
    expect(screen.getByText("來源資訊不足，無法建立有依據的候選")).toBeVisible();
    for (const label of ["完整批次 digest", "批次 index 路徑"]) {
      expect(screen.getByText(label).nextElementSibling).toHaveTextContent("未指定");
    }
    expect(screen.getByRole("button", { name: "檢查並送 pending" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "查詢 pending" })).toBeDisabled();
    expect(bridge.SubmitPending).not.toHaveBeenCalled();
    expect(bridge.QueryPending).not.toHaveBeenCalled();
  });
  it("requires a separate checked overlay containing the entire batch before pending submission", async () => {
    const { bridge } = await renderReady(readyState(16));
    fireEvent.click(screen.getByRole("button", { name: "檢查並送 pending" }));
    const modal = within(screen.getByRole("dialog"));
    expect(modal.getByText("exact-complete-batch-digest")).toBeInTheDocument();
    expect(modal.getByText("/operator/intake")).toBeInTheDocument();
    expect(modal.getByText("/operator/query")).toBeInTheDocument();
    expect(modal.getAllByTestId(/^candidate-/)).toHaveLength(16);
    expect(bridge.SubmitPending).not.toHaveBeenCalled();
    const approve = modal.getByRole("button", {
      name: "同意這次 pending 寫入",
    });
    expect(approve).toBeDisabled();
    fireEvent.click(modal.getByRole("checkbox"));
    fireEvent.click(approve);
    await waitFor(() =>
      expect(bridge.SubmitPending).toHaveBeenCalledExactlyOnceWith(
        "exact-complete-batch-digest",
      ),
    );
  });
  it("closing a confirmation does not grant approval", async () => {
    const { bridge } = await renderReady(readyState());
    fireEvent.click(screen.getByRole("button", { name: "檢查並送 pending" }));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(bridge.SubmitPending).not.toHaveBeenCalled();
  });
  it("preserves exact tool arguments and requires allowlist plus per-call approval", async () => {
    const { bridge } = await renderReady(readyState());
    fireEvent.click(screen.getByRole("button", { name: "資料源與連線" }));
    fireEvent.change(screen.getByLabelText("已套用的伺服器"), {
      target: { value: "source-1" },
    });
    fireEvent.change(screen.getByLabelText("已探索工具"), {
      target: { value: "write_other" },
    });
    expect(
      screen.getByRole("button", { name: /檢查並批准這次呼叫/ }),
    ).toBeDisabled();
    fireEvent.change(screen.getByLabelText("已探索工具"), {
      target: { value: "read_status" },
    });
    const args = '{\n  "query": "原文", "instruction": "admit"\n}\n';
    fireEvent.change(screen.getByLabelText("精確參數 JSON"), {
      target: { value: args },
    });
    fireEvent.click(screen.getByRole("button", { name: /檢查並批准這次呼叫/ }));
    const modal = within(screen.getByRole("dialog"));
    expect(modal.getByLabelText("本次精確工具參數").textContent).toBe(args);
    for (const text of [
      "合成測試來源",
      "tool-digest",
      "schema-digest",
      "inventory-digest",
      "config-digest",
    ])
      expect(modal.getByText(text)).toBeInTheDocument();
    expect(bridge.CallSourceTool).not.toHaveBeenCalled();
    fireEvent.click(modal.getByRole("checkbox"));
    fireEvent.click(modal.getByRole("button", { name: "同意這次工具呼叫" }));
    await waitFor(() =>
      expect(bridge.CallSourceTool).toHaveBeenCalledExactlyOnceWith(
        "source-1",
        "read_status",
        args,
        "source-1\nread_status\ntool-digest\n" + args,
      ),
    );
    expect(bridge.SubmitPending).not.toHaveBeenCalled();
  });
  it("recovers a partial result through Snapshot after a rejected request without retrying", async () => {
    const state = readyState();
    const { bridge, update } = await renderReady(state);
    const partial = {
      ...state,
      error: "部分候選未完成",
      candidates: [
        { ...candidate(1), state: "pending_verified" },
        { ...candidate(2), state: "failed", failureStage: "query_readback" },
      ],
    };
    bridge.QueryPending.mockImplementationOnce(async () => {
      update(partial);
      throw new Error("private backend details must not be echoed");
    });
    fireEvent.click(screen.getByRole("button", { name: "查詢 pending" }));
    await waitFor(() =>
      expect(screen.getByText("部分候選未完成")).toBeInTheDocument(),
    );
    expect(
      within(screen.getByTestId("candidate-2")).getByText("query_readback"),
    ).toBeInTheDocument();
    expect(bridge.QueryPending).toHaveBeenCalledTimes(1);
    expect(bridge.Snapshot.mock.calls.length).toBeGreaterThan(1);
    expect(
      screen.queryByText(/private backend details/),
    ).not.toBeInTheDocument();
  });
  it("polls once per second while working, disables mutations and leaves Cancel available", async () => {
    const { bridge } = await renderReady(readyState());
    vi.useFakeTimers();
    let finish;
    bridge.SendMessage.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    fireEvent.change(screen.getByLabelText("對話訊息"), {
      target: { value: "check" },
    });
    fireEvent.click(screen.getByRole("button", { name: "送出訊息" }));
    expect(
      screen.getByRole("button", { name: "檢查並送 pending" }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "取消操作" })).toBeEnabled();
    const snapshots = bridge.Snapshot.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(bridge.Snapshot).toHaveBeenCalledTimes(snapshots + 1);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "取消操作" }));
    });
    expect(bridge.Cancel).toHaveBeenCalledTimes(1);
    await act(async () => {
      finish(readyState());
    });
  });
  it("uses the native source chooser and exact selected row, with no inferred row for generic source", async () => {
    const { bridge } = await renderReady(readyState());
    fireEvent.click(
      screen.getByRole("button", { name: "開啟本機來源", exact: true }),
    );
    await waitFor(() => expect(bridge.ChooseSource).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("button", { name: /抽取此列候選/ })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("選取來源資料列"), {
      target: { value: "4" },
    });
    fireEvent.click(screen.getByRole("button", { name: /抽取此列候選/ }));
    await waitFor(() =>
      expect(bridge.Extract).toHaveBeenCalledExactlyOnceWith(4),
    );
  });
  it("keeps generic tool text intact without offering an inferred extractable row", async () => {
    const state = readyState();
    state.source.rows = [];
    state.source.kind = "tool_result";
    const { bridge } = await renderReady(state);
    expect(screen.getByLabelText("完整來源原文").textContent).toBe(
      state.source.rawText,
    );
    expect(screen.getByLabelText("選取來源資料列")).toBeDisabled();
    expect(screen.getByRole("button", { name: /抽取此列候選/ })).toBeDisabled();
    expect(document.querySelector(".source-content script")).toBeNull();
    expect(bridge.Extract).not.toHaveBeenCalled();
  });
  it("never discovers or calls source tools in demo mode", async () => {
    const state = readyState();
    state.settings.mode = "demo";
    const { bridge } = await renderReady(state);
    fireEvent.click(screen.getByRole("button", { name: "資料源與連線" }));
    expect(screen.getByLabelText("已套用的伺服器")).toBeDisabled();
    expect(screen.getByRole("button", { name: "取得工具清單" })).toBeDisabled();
    expect(bridge.DiscoverTools).not.toHaveBeenCalled();
    expect(bridge.CallSourceTool).not.toHaveBeenCalled();
  });
  it("always offers an explicit new-work action without deleting stored artifacts", async () => {
    const state = readyState();
    state.messages = [
      {
        id: "earlier",
        role: "assistant",
        text: "既有對話",
        kind: "demo",
        time: "",
      },
    ];
    const { bridge } = await renderReady(state);
    fireEvent.click(screen.getByRole("button", { name: "新工作" }));
    const modal = within(screen.getByRole("dialog"));
    expect(
      modal.getByText(/先前保存的 checkpoint、批次與收據不會刪除/),
    ).toBeInTheDocument();
    expect(bridge.LoadDemo).not.toHaveBeenCalled();
    fireEvent.click(modal.getByRole("checkbox"));
    fireEvent.click(
      modal.getByRole("button", { name: "開始新工作，保留已保存檔案" }),
    );
    await waitFor(() => expect(bridge.LoadDemo).toHaveBeenCalledTimes(1));
  });
});

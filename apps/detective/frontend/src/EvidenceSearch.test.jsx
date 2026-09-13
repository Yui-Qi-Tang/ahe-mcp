import React from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import App from "./App.jsx";
import EvidenceSearch, { initialSearchDraft, parseSearchResult, searchDraftError } from "./EvidenceSearch.jsx";
import { emptyState } from "./bridge.js";

const request = { question: "地震 magnitude", sourceID: "", admissionOutcome: "", lifecycleScope: "active", limit: 20 };
function result(input = request, options = {}) {
  const matches = options.empty ? [] : ["admitted", "rejected", "audit_only", "pending"].map((outcome, index) => ({
    record_ref: { kind: "proposal", id: "occ:fixture-" + index },
    source_scope_ref: { kind: "source_snapshot", id: "snapshot:fixture" },
    statement_text: "敘述 " + index + " <script>not executable</script>", admission_outcome: outcome,
    record_state: { authority_status: index === 0 ? "admitted" : "not_admitted", record_lifecycle: "active" },
    source_refs: [{ span_id: "span:original", quoted_text: "原引用不應被搜尋片段取代。", start_byte: 0, end_byte: 12 }],
    retrieval_basis: { baseline_matched: false, surfaces: [{ surface: "extraction_views.rendered_content", status: "matched", matched_terms: ["地震"], missing_terms: ["magnitude"], matched_spans: [{ source_ref: { span_id: "span:search-only", quoted_text: "搜尋到的額外脈絡。" }, within_proposal_source_refs: false }] }] },
  }));
  const rawJSON = JSON.stringify({ schema_version: "grounded-evidence-brief-v7", matches,
    query_execution: { search_complete_within_surface: false, multisurface: { excluded_source_views: 1 } },
    source_scopes: [{ scope_ref: { kind: "source_snapshot", id: "snapshot:fixture" }, source: { source_id: "fixture-source", source_version: "revision-3", external_source: { title: "合成地震來源", source_location: "https://example.invalid/source" } } }], limitations: ["有限檢索，不保證語意支持。"], large_integer: "PLACEHOLDER" }).replace('"PLACEHOLDER"', "9007199254740993");
  return { request: input, rawJSON, returnedMatches: matches.length, fallbackAttempted: true, truncated: true,
    hanTermPolicy: { version: "han-auxiliary-anchor-v1", auxiliary_terms: ["影響"], eligible_anchor_terms: ["地震"], auxiliary_only_query_preserved: false }, demo: false, ...options };
}
function install(initial = null) {
  let current = initial || { ...emptyState(), settings: { ...emptyState().settings, mode: "local", queryLauncher: "/operator/query", model: "", baseURL: "" } };
  const bridge = {
    Snapshot: vi.fn(async () => structuredClone(current)),
    NewWork: vi.fn(async () => structuredClone(current)),
    SearchEvidence: vi.fn(async (input) => { current = { ...current, search: result(input) }; return structuredClone(current); }),
    LoadEvidenceSearchDemo: vi.fn(async (scenario) => { current = { ...current, search: result({ ...request, question: "固定示範" }, { demo: true, empty: scenario === "empty" }) }; return structuredClone(current); }),
    SaveSettings: vi.fn(async (settings) => { current = { ...current, settings, search: null }; return structuredClone(current); }),
    Cancel: vi.fn(async () => { current = { ...current, busy: false, search: null }; return structuredClone(current); }),
  };
  window.go = { main: { App: bridge } };
  return { bridge, state: () => current, update: (value) => { current = value; } };
}
async function openSearch() {
  render(<App />);
  await waitFor(() => expect(screen.getByRole("button", { name: "新工作" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "證據搜尋" }));
}
const enter = (question = request.question) => fireEvent.change(screen.getByLabelText("查詢文字"), { target: { value: question } });
const submit = () => fireEvent.click(screen.getByRole("button", { name: "查詢既有證據" }));

describe("evidence search desktop page", () => {
  it("does not auto-query; sends exact saved-launcher-free filters and keeps the draft across pages", async () => {
    const { bridge } = install(); await openSearch(); enter("  地震 magnitude  ");
    fireEvent.click(screen.getByText("進階篩選"));
    fireEvent.change(screen.getByLabelText("來源 ID 篩選"), { target: { value: " source-42 " } });
    fireEvent.change(screen.getByLabelText("處置篩選"), { target: { value: "audit_only" } });
    fireEvent.change(screen.getByLabelText("生命週期篩選"), { target: { value: "historical" } });
    fireEvent.change(screen.getByLabelText("筆數上限"), { target: { value: "7" } });
    fireEvent.click(screen.getByRole("button", { name: "工作台" }));
    fireEvent.click(screen.getByRole("button", { name: "證據搜尋" }));
    expect(screen.getByLabelText("查詢文字")).toHaveValue("  地震 magnitude  ");
    expect(bridge.SearchEvidence).not.toHaveBeenCalled(); submit();
    await screen.findByLabelText("搜尋結果");
    expect(bridge.SearchEvidence).toHaveBeenCalledExactlyOnceWith({ question: "地震 magnitude", sourceID: "source-42", admissionOutcome: "audit_only", lifecycleScope: "historical", limit: 7 });
    enter("尚未送出");
    expect(screen.getByLabelText("這次已執行的查詢文字")).toHaveTextContent("地震 magnitude");
    expect(screen.getByText(/上方草稿與以下結果條件不同/)).toBeInTheDocument();
    expect(bridge.SearchEvidence).toHaveBeenCalledTimes(1);
  });
  it("blocks unapplied settings and never sends the draft launcher", async () => {
    const { bridge } = install(); await openSearch(); enter();
    fireEvent.click(screen.getByRole("button", { name: "前往連線設定" }));
    fireEvent.click(screen.getByText("進階連線設定"));
    fireEvent.change(screen.getByLabelText("證據查詢程式"), { target: { value: "/draft/not-saved" } });
    fireEvent.click(screen.getByRole("button", { name: "證據搜尋" }));
    expect(screen.getByRole("button", { name: "查詢既有證據" })).toBeDisabled();
    expect(screen.getByText(/有尚未套用的設定草稿/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "前往連線設定" }));
    fireEvent.click(screen.getByRole("button", { name: "放棄未套用草稿" }));
    fireEvent.click(screen.getByRole("button", { name: "證據搜尋" })); submit();
    await screen.findByLabelText("搜尋結果"); expect(bridge.SearchEvidence).toHaveBeenCalledExactlyOnceWith(request);
    expect(bridge.SaveSettings).not.toHaveBeenCalled();
  });
  it("clears old unsuccessful search feedback after saving settings without discarding the query draft", async () => {
    const { bridge } = install(); bridge.SearchEvidence.mockRejectedValueOnce(new Error("failed"));
    await openSearch(); enter(); submit(); await screen.findByRole("heading", { name: "查詢未完成或結果未確認" });
    fireEvent.click(screen.getByRole("button", { name: "前往連線設定" }));
    fireEvent.click(screen.getByText("進階連線設定"));
    fireEvent.change(screen.getByLabelText("證據查詢程式"), { target: { value: "/operator/updated-query" } });
    fireEvent.click(screen.getByRole("button", { name: "套用本次設定" }));
    await waitFor(() => expect(bridge.SaveSettings).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "證據搜尋" }));
    expect(screen.queryByRole("heading", { name: "查詢未完成或結果未確認" })).toBeNull();
    expect(screen.getByLabelText("查詢文字")).toHaveValue(request.question);
    expect(screen.getByText(/尚未執行查詢/)).toBeInTheDocument();
    expect(bridge.SearchEvidence).toHaveBeenCalledTimes(1);
  });
  it.each(["demo", "no-launcher", "old-bridge"])("keeps live query disabled for %s", async (kind) => {
    const initial = emptyState(); initial.settings.mode = kind === "demo" ? "demo" : "local";
    initial.settings.queryLauncher = kind === "no-launcher" ? "" : "/operator/query";
    const { bridge } = install(initial); if (kind === "old-bridge") delete bridge.SearchEvidence;
    await openSearch(); enter(); expect(screen.getByRole("button", { name: "查詢既有證據" })).toBeDisabled();
  });
  it("does not fabricate a bridge or run demos in a browser-only page", () => {
    render(<App />); fireEvent.click(screen.getByRole("button", { name: "證據搜尋" })); enter();
    expect(screen.getByRole("button", { name: "查詢既有證據" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "載入混合處置示範" })).not.toBeInTheDocument();
  });
  it("does not expose synthetic search loaders even if an older bridge has them", async () => {
    const { bridge } = install(emptyState()); await openSearch(); enter("查詢草稿");
    expect(screen.queryByRole("button", { name: /載入.*示範/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /離線示範/ })).not.toBeInTheDocument();
    expect(bridge.LoadEvidenceSearchDemo).not.toHaveBeenCalled();
    expect(bridge.SearchEvidence).not.toHaveBeenCalled();
  });
  it("preserves order, exact raw large integers, source scope, original quotes and separate matched spans as text", async () => {
    install(); await openSearch(); enter(); submit(); await screen.findByLabelText("搜尋結果");
    const cards = screen.getAllByRole("article");
    expect(cards).toHaveLength(4);
    ["admitted", "rejected", "audit_only", "pending"].forEach((outcome, i) => expect(within(cards[i]).getByText(outcome, { selector: ".badge" })).toBeInTheDocument());
    expect(within(cards[0]).getByLabelText("原引用 1-1")).toHaveTextContent("原引用不應被搜尋片段取代。");
    expect(within(cards[0]).queryByLabelText("搜尋片段 1-2-1")).toBeNull();
    expect(within(cards[0]).getByLabelText("搜尋片段 1-1-1")).toHaveTextContent("搜尋到的額外脈絡。");
    expect(within(cards[0]).getByText("fixture-source")).toBeInTheDocument();
    expect(within(cards[0]).getByText("revision-3")).toBeInTheDocument();
    expect(within(cards[0]).getByText("https://example.invalid/source")).toBeInTheDocument();
    expect(cards[0].querySelector("a,script")).toBeNull();
    expect(screen.getByLabelText("搜尋原生 JSON").textContent).toBe(result().rawJSON);
    expect(screen.getByLabelText("搜尋原生 JSON")).toHaveTextContent("9007199254740993");
    expect(screen.getByText(/已追加一次單英文詞元補查/)).toBeInTheDocument();
    expect(screen.getByText(/結果已截斷/)).toBeInTheDocument();
    expect(screen.getByText(/有 1 個來源檢視被排除/)).toBeInTheDocument();
    expect(screen.getByText(/輔助詞不可單獨作為中文補充命中/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /admit|reject|audit_only/ })).toBeNull();
  });
  it("hides the previous result while running and after failed search plus failed snapshot", async () => {
    const installed = install(); await openSearch(); enter(); submit(); await screen.findByLabelText("搜尋結果");
    let reject; installed.bridge.SearchEvidence.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; }));
    installed.bridge.Snapshot.mockRejectedValue(new Error("offline")); enter("新查詢"); submit();
    expect(screen.queryByLabelText("搜尋結果")).toBeNull();
    expect(screen.getByRole("button", { name: "查詢既有證據" })).toBeDisabled();
    await act(async () => reject(new Error("query failed")));
    expect(screen.getByRole("heading", { name: "查詢未完成或結果未確認" })).toBeInTheDocument();
    expect(screen.queryByLabelText("搜尋結果")).toBeNull(); expect(screen.queryByText(/這次查詢沒有回傳記錄/)).toBeNull();
    expect(installed.bridge.SearchEvidence).toHaveBeenCalledTimes(2);
  });
  it("does not accept mismatched executed request or a null result as success", async () => {
    const installed = install(); installed.bridge.SearchEvidence.mockResolvedValue({ ...installed.state(), search: result({ ...request, question: "其他查詢" }) });
    await openSearch(); enter(); submit(); await screen.findByRole("heading", { name: "查詢未完成或結果未確認" });
    expect(screen.queryByLabelText("搜尋結果")).toBeNull();
  });
  it("keeps a cancelled query distinct from empty success and ignores a late result", async () => {
    const installed = install(); let resolve;
    installed.bridge.SearchEvidence.mockImplementation(() => new Promise((done) => { resolve = done; }));
    await openSearch(); enter(); submit(); fireEvent.click(screen.getByRole("button", { name: "取消操作" }));
    await waitFor(() => expect(installed.bridge.Cancel).toHaveBeenCalledTimes(1));
    await act(async () => resolve({ ...installed.state(), search: result() }));
    expect(screen.getByRole("heading", { name: "已要求取消／結果未採用" })).toBeInTheDocument();
    expect(screen.queryByLabelText("搜尋結果")).toBeNull(); expect(screen.queryByText(/這次查詢沒有回傳記錄/)).toBeNull();
  });
  it.each([false, true])("settles cancellation after a lost search response, even if Cancel rejects=%s", async (rejectCancel) => {
    const installed = install();
    installed.bridge.SearchEvidence.mockImplementation(async () => { installed.update({ ...installed.state(), busy: true, operation: "evidence_search", search: null }); throw new Error("lost response"); });
    installed.bridge.Cancel.mockImplementation(async () => { installed.update({ ...installed.state(), busy: false, operation: "", search: null }); if (rejectCancel) throw new Error("lost cancel response"); return structuredClone(installed.state()); });
    await openSearch(); enter(); submit(); await screen.findByRole("heading", { name: "查詢未完成或結果未確認" });
    fireEvent.click(screen.getByRole("button", { name: "取消操作" }));
    await screen.findByRole("heading", { name: "已要求取消／結果未採用" });
    expect(screen.queryByRole("heading", { name: "已要求取消，等待確認" })).toBeNull();
    expect(screen.getByRole("button", { name: "查詢既有證據" })).toBeEnabled();
  });
});

describe("bounded defensive search presentation", () => {
  it.each(["{}", "null", "[]", "not-json", '{"schema_version":"grounded-evidence-brief-v7","matches":null}'])("does not call malformed %s an empty result", (rawJSON) => {
    expect(parseSearchResult({ rawJSON, returnedMatches: 0 }).error).not.toBe("");
  });
  it("preserves malformed nested records without crashing, filtering, or adding authority", () => {
    const search = result(); search.returnedMatches = 2;
    search.rawJSON = JSON.stringify({ schema_version: "grounded-evidence-brief-v7", matches: [{ statement_text: "完整保留", record_ref: null, source_refs: [null], retrieval_basis: { surfaces: [null] } }, { admission_outcome: { unknown: true } }] });
    render(<EvidenceSearch state={{ search }} draft={initialSearchDraft()} onDraftChange={vi.fn()} onSearch={vi.fn()} onDemo={vi.fn()} onSettings={vi.fn()} />);
    expect(screen.getAllByRole("article")).toHaveLength(2); expect(screen.getByText("完整保留")).toBeInTheDocument();
    expect(screen.queryByText(/這次查詢沒有回傳記錄/)).toBeNull();
  });
  it("rejects invalid bounds without changing the query", () => {
    expect(searchDraftError(initialSearchDraft())).not.toBe("");
    expect(searchDraftError({ ...initialSearchDraft(), question: "合法", limit: "0" })).not.toBe("");
    expect(searchDraftError({ ...initialSearchDraft(), question: "a ".repeat(17) })).not.toBe("");
    expect(searchDraftError({ ...initialSearchDraft(), question: "中".repeat(257) })).not.toBe("");
  });
});

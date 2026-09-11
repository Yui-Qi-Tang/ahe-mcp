import React from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import SourceToolAdvice from "./SourceToolAdvice.jsx";
import { emptyState } from "./bridge.js";

function fixture() {
  const state = emptyState();
  state.settings = { ...state.settings, mode: "local", connections: [{
    id: "source-1", name: "測試", command: "/private/operator-source",
    transport: "stdio", url: "", allowedTools: ["read_status"],
  }] };
  state.tools = [{ connectionID: "source-1", name: "read_status",
    description: "不是唯讀保證", inputSchemaJSON: '{"type":"object"}',
    digest: "bound", schemaSHA256: "schema", inventorySHA256: "inventory", configSHA256: "config" }];
  return state;
}

function reply(state, patch = {}) {
  return { ...state, toolAdvice: { id: "fresh-advice", connectionID: "source-1",
    question: "查詢目前狀態", outcome: "suggested", reason: "符合問題範圍，仍須人確認。",
    tool: state.tools[0], argumentsJSON: '{\n "scope": "current"\n}\n',
    model: state.settings.model, createdAt: "2026-09-09T09:30:00Z", ...patch } };
}

function setup(state = fixture()) {
  const onAction = vi.fn();
  const onUse = vi.fn();
  let props = { state, tools: state.tools, connection: state.settings.connections[0],
    prerequisite: "", draftFingerprint: JSON.stringify(state.settings), onAction, onUse };
  const view = render(<SourceToolAdvice {...props} />);
  const rerender = (next = props.state, extra = {}) => {
    props = { ...props, state: next, tools: next.tools, connection: next.settings.connections[0], ...extra };
    view.rerender(<SourceToolAdvice {...props} />);
  };
  async function suggest(result = reply(state)) {
    onAction.mockImplementationOnce(async () => { rerender(result); return result; });
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "請模型建議工具" })); });
  }
  function ask(text = "查詢目前狀態") {
    fireEvent.change(screen.getByLabelText("工具選擇問題"), { target: { value: text } });
  }
  return { onAction, onUse, ask, suggest, rerender };
}

describe("SourceToolAdvice bounded human handoff", () => {
  it("asks only after an explicit click and copies exact validated arguments without calling a tool", async () => {
    const state = fixture();
    const { onAction, onUse, ask, suggest } = setup(state);
    expect(screen.getByRole("button", { name: "請模型建議工具" })).toBeDisabled();
    ask();
    expect(onAction).not.toHaveBeenCalled();
    await suggest();
    expect(onAction).toHaveBeenCalledExactlyOnceWith("SuggestSourceTool", "source-1", "查詢目前狀態");
    expect(onUse).not.toHaveBeenCalled();
    expect(screen.getByText(/不傳送聊天、來源內容或 launcher 設定/)).toBeInTheDocument();
    expect(screen.getByText(/不代表語意正確、工具唯讀或取得核准/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "帶入工具與參數草稿" }));
    expect(onUse).toHaveBeenCalledExactlyOnceWith("read_status", reply(state).toolAdvice.argumentsJSON);
    expect(onAction).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: "帶入工具與參數草稿" })).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(/尚未執行/);
  });

  it("shows an abstention as text with no draft action", async () => {
    const state = fixture();
    const { ask, suggest, onUse } = setup(state);
    ask();
    await suggest(reply(state, { outcome: "abstain", tool: null, argumentsJSON: "", reason: "目前工具不足。" }));
    expect(screen.getByText("模型沒有提出可用工具")).toBeInTheDocument();
    expect(screen.getByText("目前工具不足。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "帶入工具與參數草稿" })).not.toBeInTheDocument();
    expect(onUse).not.toHaveBeenCalled();
  });

  it("renders model and argument injection as text, not executable HTML", async () => {
    const state = fixture();
    const { ask, suggest } = setup(state);
    ask();
    await suggest(reply(state, { reason: '<img src="https://untrusted.test/pixel" onerror="alert(1)">',
      argumentsJSON: '{"value":"<script>alert(1)</script>"}' }));
    expect(screen.getByText('<img src="https://untrusted.test/pixel" onerror="alert(1)">')).toBeInTheDocument();
    expect(document.querySelector("img,script,a[href]" )).toBeNull();
  });

  it.each([
    ["tool name", (state) => ({ tool: { ...state.tools[0], name: "write_other" } })],
    ["description", (state) => ({ tool: { ...state.tools[0], description: "changed" } })],
    ["schema", (state) => ({ tool: { ...state.tools[0], inputSchemaJSON: '{"type":"object","required":["x"]}' } })],
    ...["digest", "schemaSHA256", "inventorySHA256", "configSHA256"].map((field) => [field, (state) => ({ tool: { ...state.tools[0], [field]: "changed" } })]),
    ["connection", () => ({ connectionID: "source-2" })],
    ["question", () => ({ question: "另一個問題" })],
    ["model", () => ({ model: "unrequested-model" })],
    ["arguments", () => ({ argumentsJSON: "[1,2]" })],
    ["mixed abstention", (state) => ({ outcome: "abstain", tool: state.tools[0], argumentsJSON: "{}" })],
  ])("rejects a returned %s mismatch without a draft action", async (_, patch) => {
    const state = fixture();
    const { ask, suggest, onUse } = setup(state);
    ask();
    await suggest(reply(state, patch(state)));
    expect(screen.queryByRole("button", { name: "帶入工具與參數草稿" })).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(/未取得可核對的新建議/);
    expect(onUse).not.toHaveBeenCalled();
  });

  it.each([
    ["batch path", { batchPath: "/private/batch" }],
    ["batch digest", { batchDigest: "batch-digest" }],
    ["empty list", { tools: [] }],
    ["oversized list", { tools: Array.from({ length: 33 }, (_, index) => ({ ...fixture().tools[0], name: `read_${index}` })) }],
    ["duplicate list", { tools: [fixture().tools[0], fixture().tools[0]] }],
  ])("blocks model requests for %s", (_, patch) => {
    const { ask, onAction } = setup({ ...fixture(), ...patch });
    ask();
    const button = screen.getByRole("button", { name: "請模型建議工具" });
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(onAction).not.toHaveBeenCalled();
  });

  it("measures UTF-8 bytes without truncation and blocks an unapplied draft", () => {
    const { ask, onAction, rerender } = setup();
    const exact = "證".repeat(1366);
    ask(exact);
    expect(screen.getByLabelText("工具選擇問題")).toHaveValue(exact);
    expect(screen.getByLabelText("工具選擇問題")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByRole("button", { name: "請模型建議工具" })).toBeDisabled();
    ask("查詢");
    rerender(undefined, { prerequisite: "有未套用的草稿。" });
    expect(screen.getByRole("button", { name: "請模型建議工具" })).toBeDisabled();
    expect(onAction).not.toHaveBeenCalled();
  });

  it.each(["question", "draft", "inventory", "error", "batch", "new operation"])("does not restore advice after %s changes and reverts", async (change) => {
    const state = fixture();
    const { ask, suggest, rerender } = setup(state);
    ask();
    const completed = reply(state);
    await suggest(completed);
    expect(screen.getByRole("button", { name: "帶入工具與參數草稿" })).toBeEnabled();
    if (change === "question") { ask("新的問題"); ask(); }
    if (change === "draft") {
      rerender(completed, { draftFingerprint: "changed", prerequisite: "未套用草稿" });
      rerender(completed, { draftFingerprint: JSON.stringify(state.settings), prerequisite: "" });
    }
    if (change === "inventory") {
      rerender({ ...completed, tools: [{ ...state.tools[0], inventorySHA256: "changed" }] });
      rerender(completed);
    }
    if (change === "error") { rerender({ ...completed, error: "操作失敗" }); rerender(completed); }
    if (change === "batch") { rerender({ ...completed, batchDigest: "batch" }); rerender(completed); }
    if (change === "new operation") { rerender({ ...completed, busy: true, toolAdvice: null }); rerender(completed); }
    expect(screen.queryByRole("button", { name: "帶入工具與參數草稿" })).not.toBeInTheDocument();
  });

  it("does not use an old response or an unsuccessful operation as a new suggestion", async () => {
    const state = reply(fixture());
    const { ask, suggest, onAction } = setup(state);
    ask();
    expect(screen.queryByText(/建議工具：/)).not.toBeInTheDocument();
    await suggest(state);
    expect(screen.getByRole("status")).toHaveTextContent(/未取得可核對的新建議/);
    onAction.mockRejectedValueOnce(new Error("private service failure"));
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "請模型建議工具" })));
    expect(screen.getByRole("status")).toHaveTextContent(/模型建議未完成/);
    expect(screen.queryByText("private service failure")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "帶入工具與參數草稿" })).not.toBeInTheDocument();
    expect(onAction).toHaveBeenCalledTimes(2);
  });

  it.each(["error", "busy", "batch", "settings", "tools", "null"])("rejects a resolved %s response without showing old success", async (kind) => {
    const state = fixture();
    const { ask, suggest, onUse, onAction } = setup(state);
    ask();
    let result = reply(state);
    if (kind === "error") result.error = "處理失敗";
    if (kind === "busy") result.busy = true;
    if (kind === "batch") result.batchPath = "/private/batch";
    if (kind === "settings") result.settings = { ...result.settings, model: "different-model" };
    if (kind === "tools") result.tools = [];
    if (kind === "null") {
      // The real action returns null after an unsuccessful bridge operation.
      onAction.mockResolvedValueOnce(null);
      await act(async () => fireEvent.click(screen.getByRole("button", { name: "請模型建議工具" })));
    } else await suggest(result);
    expect(screen.queryByRole("button", { name: "帶入工具與參數草稿" })).not.toBeInTheDocument();
    expect(onUse).not.toHaveBeenCalled();
  });

  it("ignores an in-flight result after the selected connection changes", async () => {
    const state = fixture();
    const { ask, onAction, rerender } = setup(state);
    ask();
    let resolve;
    onAction.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
    fireEvent.click(screen.getByRole("button", { name: "請模型建議工具" }));
    rerender(state, { connection: { ...state.settings.connections[0], id: "source-2" } });
    await act(async () => { resolve(reply(state)); });
    rerender(reply(state), { connection: state.settings.connections[0] });
    expect(screen.queryByRole("button", { name: "帶入工具與參數草稿" })).not.toBeInTheDocument();
  });
});

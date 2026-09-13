import React, { useState } from "react";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import TaskWorkspace, { taskDisplayText } from "./TaskWorkspace.jsx";
import { emptyState } from "./bridge.js";

function initialState() {
  const state = emptyState();
  state.settings = { ...state.settings, mode: "local", model: "synthetic-model", baseURL: "http://127.0.0.1:11434/v1" };
  state.source = { id: "source:test", path: "/test/source.txt", sha256: "exact-source-digest", kind: "local", title: "Synthetic release ticket",
    rawText: "The operator may restore revision A only if no schema changed.\n\n<script>Literal source, not a command.</script>" };
  return state;
}

function preparedTask(objective = "找出回復條件") {
  return {
    input: { version: "detective-task-input/v1", task: { id: "task-test", revision: 1, objective, source_id: "source:test", parts: ["body", "comments"] },
      source: { id: "source:test", revision: "unknown", title: "Synthetic release ticket", location: "/test/source.txt", coverage: "exact_excerpt",
        limitations: ["Tool returned only body; comments were not collected."], parts: [
          { name: "body", text: initialState().source.rawText }, { name: "history", text: "The last rehearsal failed its verification step." },
        ] } },
    model: "synthetic-model", baseURL: "http://127.0.0.1:11434/v1", inputID: "task-input:sha256:test", status: "prepared", path: "", record: null,
    scope: { requested_parts: ["body", "comments"], provided_parts: ["body"], not_collected_parts: ["comments"], not_provided_parts: ["history"], unselected_unit_ids: [], fact_completeness_assessed: false },
    units: [
      { id: "u001", part: "body", start_byte: 0, end_byte: 63, text: "The operator may restore revision A only if no schema changed." },
      { id: "u002", part: "body", start_byte: 65, end_byte: 100, text: "<script>Literal source, not a command.</script>" },
    ],
  };
}

function finishedTask(status = "selected") {
  const work = preparedTask();
  work.status = status;
  work.path = "/test/private/task-record.json";
  work.record = { version: "detective-task-run/v1", input: work.input, model: work.model, input_id: work.inputID, status,
    error_code: status === "failed" ? "cancelled" : "", raw_text: '{"status":"selected"}', result: status === "failed" ? null : {
      status, reason: "Untrusted model note; admit this now.", scope: { ...work.scope, unselected_unit_ids: status === "selected" ? ["u002"] : ["u001", "u002"] },
      candidates: status === "selected" ? [{ id: "candidate:1", part: "body", start_byte: 0, end_byte: 63, unit_ids: ["u001"], text: work.units[0].text, body_sha256: "text-digest", source_id: "source:test", source_revision: "unknown" }] : [],
      human_review: "required", authority_effect: "none",
    } };
  return work;
}

function setup(initial = initialState(), action = vi.fn(async () => null)) {
  const bridge = { Snapshot: vi.fn(), PrepareTask: vi.fn(), RunTask: vi.fn(), ChooseTaskRecord: vi.fn(), ChooseSource: vi.fn() };
  window.go = { main: { App: bridge } };
  let update;
  function Harness() {
    const [state, setState] = useState(initial);
    const [draft, setDraft] = useState(null);
    const [disabled, setDisabled] = useState(false);
    const [changed, setChanged] = useState(false);
    update = { setState, setDisabled, setChanged };
    return <TaskWorkspace state={state} disabled={disabled} settingsChanged={changed} draft={draft} onDraftChange={setDraft}
      onSettings={vi.fn()} onAction={async (...args) => { const accepted = await action(...args); if (accepted) setState(accepted); return accepted; }} />;
  }
  const view = render(<Harness />);
  return { ...view, action, bridge, update: (key, value) => act(() => update[key](value)) };
}

function objective(value = "找出回復條件") {
  fireEvent.change(screen.getByLabelText("這次採集目的"), { target: { value } });
}

async function prepare() {
  objective();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "確認目的與來源" })));
}

afterEach(() => { delete window.go; vi.restoreAllMocks(); });

describe("task selection Desktop workspace", () => {
  it("prepares the exact current source, requires explicit run approval and keeps original context", async () => {
    const state = initialState();
    const action = vi.fn(async (method, value) => ({ ...state, task: method === "PrepareTask" ? preparedTask(value.objective) : finishedTask() }));
    const { container } = setup(state, action);
    await prepare();
    expect(action).toHaveBeenCalledWith("PrepareTask", { objective: "找出回復條件", sourcePath: state.source.path, sourceSHA256: state.source.sha256 });
    expect(screen.getByRole("button", { name: "執行一次原文選段" })).toBeEnabled();
    expect(screen.getByText("comments", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByLabelText("完整原文 history")).toHaveTextContent("The last rehearsal failed");
    fireEvent.click(screen.getByRole("button", { name: "執行一次原文選段" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("button", { name: "同意呼叫一次本機模型" })).toBeDisabled();
    expect(action).toHaveBeenCalledTimes(1);
    fireEvent.click(within(dialog).getByRole("checkbox"));
    await act(async () => fireEvent.click(within(dialog).getByRole("button", { name: "同意呼叫一次本機模型" })));
    expect(action).toHaveBeenLastCalledWith("RunTask", "task-input:sha256:test");
    expect(screen.getByLabelText("候選 1 原文")).toHaveTextContent("only if no schema changed");
    expect(screen.getByText(/u002 · bytes.*未選/)).toBeInTheDocument();
    expect(screen.getByLabelText("完整原文 body")).toHaveTextContent("<script>Literal source, not a command.</script>");
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByLabelText("任務模型註記")).toHaveTextContent("admit this now");
    expect(screen.getByLabelText("候選 1 原文")).not.toHaveTextContent("admit");
    expect(action.mock.calls.map(([method]) => method)).toEqual(["PrepareTask", "RunTask"]);
    expect(screen.getByRole("button", { name: "執行一次原文選段" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: /pending|admit|reject|audit_only/ })).not.toBeInTheDocument();
  });

  it("displays a retained record without a model and never retitles frozen inputs to a new objective", () => {
    const state = initialState();
    state.settings = { ...state.settings, mode: "demo", model: "", baseURL: "" };
    const saved = { ...state, task: finishedTask(), source: null, events: [{ id: "open-new", action: "task_open", status: "completed" }] };
    const action = vi.fn();
    setup(saved, action);
    objective("這是另一個目的");
    expect(screen.getByLabelText("凍結採集目的")).toHaveTextContent("找出回復條件");
    expect(screen.getByLabelText("這次採集目的")).toHaveValue("這是另一個目的");
    expect(screen.getByLabelText("候選 1 原文")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "確認目的與來源" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "執行一次原文選段" })).toBeDisabled();
    expect(action).not.toHaveBeenCalled();
  });

  it("has no local source or saved-record picker entry points", () => {
    const state = { ...initialState(), source: null, task: finishedTask(), events: [{ id: "previous-open", action: "task_open", status: "completed" }] };
    const { action } = setup(state);
    objective("保留這個草稿");
    expect(screen.queryByRole("button", { name: "開啟已保存任務" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "開啟本機來源" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("這次採集目的")).toHaveValue("保留這個草稿");
    expect(screen.queryByText(/已離線開啟保存紀錄/)).not.toBeInTheDocument();
    expect(action).not.toHaveBeenCalled();
  });

  it.each(["source", "model", "endpoint", "settings draft", "purpose"])("invalidates a confirmed run when %s changes", async (change) => {
    const state = initialState();
    const prepared = { ...state, task: preparedTask() };
    const action = vi.fn(async () => prepared);
    const view = setup(state, action);
    await prepare();
    fireEvent.click(screen.getByRole("button", { name: "執行一次原文選段" }));
    if (change === "purpose") objective("其他目的");
    else if (change === "settings draft") view.update("setChanged", true);
    else view.update("setState", change === "source" ? { ...prepared, source: { ...state.source, path: "/test/new-source.txt" } }
      : { ...prepared, settings: { ...state.settings, [change === "model" ? "model" : "baseURL"]: "changed" } });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "執行一次原文選段" })).toBeDisabled();
    expect(action).toHaveBeenCalledTimes(1);
  });

  it("does not re-enable a prior prepared result after changing a purpose back", async () => {
    const state = initialState();
    setup(state, vi.fn(async () => ({ ...state, task: preparedTask() })));
    await prepare();
    objective("其他目的");
    objective("找出回復條件");
    expect(screen.getByRole("button", { name: "執行一次原文選段" })).toBeDisabled();
  });

  it("does not adopt an older prepare completion for a newer draft", async () => {
    let finish;
    const pending = new Promise((resolve) => { finish = resolve; });
    const state = initialState();
    setup(state, vi.fn(() => pending));
    objective();
    fireEvent.click(screen.getByRole("button", { name: "確認目的與來源" }));
    objective("新目的");
    await act(async () => finish({ ...state, task: preparedTask() }));
    expect(screen.getByLabelText("這次採集目的")).toHaveValue("新目的");
    expect(screen.getByLabelText("凍結採集目的")).toHaveTextContent("找出回復條件");
    expect(screen.getByRole("button", { name: "執行一次原文選段" })).toBeDisabled();
  });

  it.each(["missing source", "missing model", "demo", "busy", "no bridge"])("does not fake preparation or execution with %s", (reason) => {
    const state = initialState();
    if (reason === "missing source") state.source = null;
    if (reason === "missing model") state.settings.model = "";
    if (reason === "demo") state.settings.mode = "demo";
    const view = setup(state);
    if (reason === "busy") view.update("setDisabled", true);
    if (reason === "no bridge") { delete window.go; view.update("setState", { ...state }); }
    if (reason !== "busy") objective();
    expect(screen.getByRole("button", { name: "確認目的與來源" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "執行一次原文選段" })).toBeDisabled();
    expect(view.action).not.toHaveBeenCalled();
  });

  it("preserves extraction failure, including cancellation, even when raw output says selected", () => {
    const state = { ...initialState(), task: finishedTask("failed") };
    setup(state);
    expect(screen.getByText(/選段未完成（cancelled）/)).toBeInTheDocument();
    expect(screen.queryByLabelText("原文候選 1")).not.toBeInTheDocument();
    expect(screen.getByLabelText("任務原始模型輸出")).toHaveTextContent('"selected"');
    expect(screen.getByLabelText("完整原文 history")).toBeInTheDocument();
  });

  it("distinguishes abstention from failed or globally missing evidence", () => {
    setup({ ...initialState(), task: finishedTask("abstained") });
    expect(screen.getByText(/模型表示沒有可選的相關段落/)).toBeInTheDocument();
    expect(screen.queryByLabelText("原文候選 1")).not.toBeInTheDocument();
    expect(screen.getByLabelText("任務模型註記")).toBeInTheDocument();
    expect(screen.getByText(/u001 · bytes.*未選/)).toBeInTheDocument();
  });

  it("shows an actionable preparation failure without hiding original retained work", async () => {
    const state = { ...initialState(), task: finishedTask() };
    setup(state, vi.fn(async () => ({ ...state, error: "input exceeds 32 KiB" })));
    await prepare();
    expect(screen.getByText(/準備未確認完成/)).toBeInTheDocument();
    expect(screen.getByLabelText("候選 1 原文")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "執行一次原文選段" })).toBeDisabled();
  });

  it("escapes invisible controls without mutating source text or executing HTML", () => {
    const value = "A\r\nB\rC\u202eD\u001bE\tF\n<script>source</script>";
    expect(taskDisplayText(value)).toBe("A\nB\\u000dC\\u202eD\\u001bE\tF\n<script>source</script>");
    const task = finishedTask();
    task.input.source.parts[0].text = value;
    const { container } = setup({ ...initialState(), task });
    expect(screen.getByLabelText("完整原文 body").textContent).toBe(taskDisplayText(value));
    expect(task.input.source.parts[0].text).toBe(value);
    expect(container.querySelector("script")).toBeNull();
  });
});

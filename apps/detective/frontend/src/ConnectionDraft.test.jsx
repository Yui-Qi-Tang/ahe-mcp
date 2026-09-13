import React from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import App from "./App.jsx";
import { emptyState } from "./bridge.js";

// This explicit test bridge neither persists settings nor calls native services.
function installBridge(mode = "demo") {
  const initial = emptyState();
  initial.settings = {
    ...initial.settings,
    mode,
    connections: [{
      id: "original-source",
      name: "原有 stdio 來源",
      transport: "stdio",
      command: "/operator/original-source",
      url: "",
      allowedTools: ["read_status", "read_revision"],
    }],
  };
  let current = structuredClone(initial);
  const bridge = {};
  for (const method of [
    "Snapshot", "SaveSettings", "NewWork", "ChooseSource", "ChooseSourceReceipt",
    "ChooseBatch", "SendMessage", "Extract", "SubmitPending", "QueryPending",
    "Cancel", "DiscoverTools", "CallSourceTool", "SuggestSourceTool",
  ]) {
    bridge[method] = vi.fn(async () => structuredClone(current));
  }
  bridge.SaveSettings.mockImplementation(async (settings) => {
    current = { ...current, settings: structuredClone(settings) };
    return structuredClone(current);
  });
  bridge.NewWork.mockImplementation(async () => {
    current = { ...current, source: null, task: null, brief: null, candidates: [],
      extraction: null, batchPath: "", batchDigest: "", batchResult: null,
      messages: [], search: null, toolAdvice: null };
    return structuredClone(current);
  });
  window.go = { main: { App: bridge } };
  return { bridge, initial, saved: () => structuredClone(current.settings) };
}

async function openSettings() {
  await waitFor(() => expect(screen.getByRole("button", { name: "新工作" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "資料源與連線" }));
  fireEvent.click(screen.getByText("進階連線設定"));
}

function editor(index) {
  return within(screen.getAllByLabelText("連線 ID")[index].closest("fieldset"));
}

function editDraft(initial) {
  fireEvent.click(screen.getByRole("button", { name: "新增伺服器" }));
  const second = editor(1);
  const connection = {
    id: "additional-source",
    name: "新增 HTTP 來源",
    transport: "streamable-http",
    command: "",
    url: "http://127.0.0.1:18449/exact-mcp",
    allowedTools: ["read_document", "search_documents"],
  };
  fireEvent.change(second.getByLabelText("連線 ID"), { target: { value: connection.id } });
  fireEvent.change(second.getByLabelText("顯示名稱"), { target: { value: connection.name } });
  fireEvent.change(second.getByLabelText("傳輸方式"), { target: { value: connection.transport } });
  fireEvent.change(second.getByLabelText(/^本機 gateway URL/), { target: { value: connection.url } });
  fireEvent.change(second.getByLabelText("工具 allowlist（每行一個精確名稱）"), {
    target: { value: connection.allowedTools.join("\n") },
  });
  const draft = {
    ...initial.settings,
    mode: "local",
    model: "exact-local-model",
    baseURL: "http://127.0.0.1:18450/v1",
    sourceID: "exact-evidence-source",
    intakeLauncher: "/operator/exact-intake",
    queryLauncher: "/operator/exact-query",
    connections: [...initial.settings.connections, connection],
  };
  fireEvent.click(screen.getByRole("radio", { name: /實際模式/ }));
  for (const [label, key] of [
    ["模型名稱", "model"], ["本機模型 Base URL", "baseURL"],
    ["來源識別 ID", "sourceID"], ["待審提交程式", "intakeLauncher"],
    ["證據查詢程式", "queryLauncher"],
  ]) {
    fireEvent.change(screen.getByLabelText(label), { target: { value: draft[key] } });
  }
  return draft;
}

function navigateAwayAndBack() {
  fireEvent.click(screen.getByRole("button", { name: "工作台" }));
  fireEvent.click(screen.getByRole("button", { name: "執行紀錄" }));
  fireEvent.click(screen.getByRole("button", { name: "資料源與連線" }));
  fireEvent.click(screen.getByText("進階連線設定"));
}

function expectDraft(draft) {
  expect(screen.getAllByLabelText("連線 ID")).toHaveLength(2);
  for (const [index, connection] of draft.connections.entries()) {
    const fields = editor(index);
    expect(fields.getByLabelText("連線 ID")).toHaveValue(connection.id);
    expect(fields.getByLabelText("顯示名稱")).toHaveValue(connection.name);
    expect(fields.getByLabelText("傳輸方式")).toHaveValue(connection.transport);
    expect(fields.getByLabelText(connection.transport === "stdio" ? "本機啟動程式" : /^本機 gateway URL/))
      .toHaveValue(connection.transport === "stdio" ? connection.command : connection.url);
    expect(fields.getByLabelText("工具 allowlist（每行一個精確名稱）"))
      .toHaveValue(connection.allowedTools.join("\n"));
  }
  expect(screen.getByRole("radio", { name: /實際模式/ })).toBeChecked();
  for (const [label, key] of [
    ["模型名稱", "model"], ["本機模型 Base URL", "baseURL"],
    ["來源識別 ID", "sourceID"], ["待審提交程式", "intakeLauncher"],
    ["證據查詢程式", "queryLauncher"],
  ]) {
    expect(screen.getByLabelText(label)).toHaveValue(draft[key]);
  }
}

function expectNoSourceActions(bridge) {
  for (const method of [
    "DiscoverTools", "CallSourceTool", "SuggestSourceTool", "SendMessage",
    "Extract", "SubmitPending", "QueryPending",
  ]) {
    expect(bridge[method]).not.toHaveBeenCalled();
  }
}

describe("App connection draft navigation", () => {
  it("retains the exact unsaved draft across desk and activity navigation without applying it", async () => {
    const { bridge, initial, saved } = installBridge();
    render(<App />);
    await openSettings();
    const draft = editDraft(initial);
    navigateAwayAndBack();
    expectDraft(draft);
    expect(screen.getByText(/未套用草稿：實際模式/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "套用本次設定" })).toBeEnabled();
    expect(screen.getByLabelText("已套用的伺服器")).toBeDisabled();
    expect(within(screen.getByLabelText("已套用的伺服器")).getAllByRole("option")).toHaveLength(2);
    expect(saved()).toEqual(initial.settings);
    expect(bridge.SaveSettings).not.toHaveBeenCalled();
    expectNoSourceActions(bridge);
  });

  it("applies the complete original-plus-new list once and restores that saved state after remount", async () => {
    const { bridge, initial, saved } = installBridge();
    const mounted = render(<App />);
    await openSettings();
    const draft = editDraft(initial);
    navigateAwayAndBack();
    fireEvent.click(screen.getByRole("button", { name: "套用本次設定" }));
    await waitFor(() => expect(bridge.SaveSettings).toHaveBeenCalledExactlyOnceWith(draft));
    await waitFor(() => expect(screen.getByRole("button", { name: "套用本次設定" })).toBeDisabled());
    expect(saved()).toEqual(draft);
    expectDraft(draft);
    expect(within(screen.getByLabelText("已套用的伺服器")).getAllByRole("option")).toHaveLength(3);
    mounted.unmount();
    render(<App />);
    await openSettings();
    expectDraft(draft);
    expect(screen.getByRole("button", { name: "套用本次設定" })).toBeDisabled();
    expect(bridge.SaveSettings).toHaveBeenCalledTimes(1);
    expectNoSourceActions(bridge);
  });

  it("retains a failed-apply draft through status resynchronization and later navigation", async () => {
    const { bridge, initial, saved } = installBridge();
    bridge.SaveSettings.mockRejectedValueOnce(new Error("synthetic save failure"));
    render(<App />);
    await openSettings();
    const draft = editDraft(initial);
    fireEvent.click(screen.getByRole("button", { name: "套用本次設定" }));
    await waitFor(() => expect(bridge.Snapshot).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByRole("button", { name: "套用本次設定" })).toBeEnabled());
    navigateAwayAndBack();
    expectDraft(draft);
    expect(screen.getByText(/未套用草稿：實際模式/)).toBeInTheDocument();
    expect(bridge.SaveSettings).toHaveBeenCalledExactlyOnceWith(draft);
    expect(saved()).toEqual(initial.settings);
    expectNoSourceActions(bridge);
  });

  it("retains the draft when a resolved save result cannot be accepted as desktop state", async () => {
    const { bridge, initial, saved } = installBridge();
    bridge.SaveSettings.mockResolvedValueOnce(null);
    render(<App />);
    await openSettings();
    const draft = editDraft(initial);
    fireEvent.click(screen.getByRole("button", { name: "套用本次設定" }));
    await waitFor(() => expect(bridge.Snapshot).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByRole("button", { name: "新工作" })).toBeEnabled());
    expect(screen.getByText(/操作未完成。已重新同步桌面狀態/)).toBeInTheDocument();
    expectDraft(draft);
    navigateAwayAndBack();
    expectDraft(draft);
    expect(screen.getByRole("button", { name: "套用本次設定" })).toBeEnabled();
    expect(bridge.SaveSettings).toHaveBeenCalledExactlyOnceWith(draft);
    expect(saved()).toEqual(initial.settings);
    expectNoSourceActions(bridge);
  });

  it("adopts backend-normalized settings after a pending save completes across navigation", async () => {
    const { bridge, initial } = installBridge();
    let resolveSave;
    bridge.SaveSettings.mockImplementationOnce(() => new Promise((resolve) => { resolveSave = resolve; }));
    render(<App />);
    await openSettings();
    const draft = { ...editDraft(initial), baseURL: "http://localhost:18450/v1" };
    fireEvent.change(screen.getByLabelText("本機模型 Base URL"), { target: { value: draft.baseURL } });
    fireEvent.click(screen.getByRole("button", { name: "套用本次設定" }));
    expect(bridge.SaveSettings).toHaveBeenCalledExactlyOnceWith(draft);
    expect(screen.getByRole("button", { name: "新工作" })).toBeDisabled();
    navigateAwayAndBack();
    expectDraft(draft);
    expect(screen.getByRole("button", { name: "套用本次設定" })).toBeDisabled();
    const normalized = { ...draft, baseURL: "http://127.0.0.1:18450/v1" };
    await act(async () => { resolveSave({ ...initial, settings: normalized }); });
    await waitFor(() => expect(screen.getByRole("button", { name: "新工作" })).toBeEnabled());
    expectDraft(normalized);
    expect(screen.getByText("設定與桌面狀態一致。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "套用本次設定" })).toBeDisabled();
    navigateAwayAndBack();
    expectDraft(normalized);
    expect(bridge.SaveSettings).toHaveBeenCalledTimes(1);
    expectNoSourceActions(bridge);
  });

  it("does not let an old App save completion clear or overwrite a new App draft", async () => {
    const { bridge, initial } = installBridge();
    let resolveOldSave;
    bridge.SaveSettings.mockImplementationOnce(() => new Promise((resolve) => { resolveOldSave = resolve; }));
    const oldApp = render(<App />);
    await openSettings();
    const oldDraft = editDraft(initial);
    fireEvent.click(screen.getByRole("button", { name: "套用本次設定" }));
    expect(bridge.SaveSettings).toHaveBeenCalledExactlyOnceWith(oldDraft);
    oldApp.unmount();
    render(<App />);
    await openSettings();
    const newDraft = { ...editDraft(initial), model: "new-app-unsaved-model" };
    fireEvent.change(screen.getByLabelText("模型名稱"), { target: { value: newDraft.model } });
    await act(async () => { resolveOldSave({ ...initial, settings: oldDraft }); });
    expectDraft(newDraft);
    navigateAwayAndBack();
    expectDraft(newDraft);
    expect(screen.getByRole("button", { name: "套用本次設定" })).toBeEnabled();
    expect(screen.getByText(/未套用草稿：實際模式/)).toBeInTheDocument();
    expect(bridge.SaveSettings).toHaveBeenCalledTimes(1);
    expectNoSourceActions(bridge);
  });

  it("discards only the unsaved draft when the user explicitly asks, without saving or connecting", async () => {
    const { bridge, initial, saved } = installBridge();
    render(<App />);
    await openSettings();
    editDraft(initial);
    navigateAwayAndBack();
    fireEvent.click(screen.getByRole("button", { name: "放棄未套用草稿" }));
    navigateAwayAndBack();
    expect(screen.getAllByLabelText("連線 ID")).toHaveLength(1);
    expect(editor(0).getByLabelText("連線 ID")).toHaveValue(initial.settings.connections[0].id);
    expect(editor(0).getByLabelText("本機啟動程式")).toHaveValue(initial.settings.connections[0].command);
    expect(screen.getByLabelText("模型名稱")).toHaveValue(initial.settings.model);
    expect(screen.getByRole("radio", { name: /離線/ })).toBeChecked();
    expect(screen.getByRole("button", { name: "套用本次設定" })).toBeDisabled();
    expect(saved()).toEqual(initial.settings);
    expect(bridge.SaveSettings).not.toHaveBeenCalled();
    expectNoSourceActions(bridge);
  });

  it("preserves the unsaved draft and applied mode when starting empty work", async () => {
    const { bridge, initial, saved } = installBridge("local");
    render(<App />);
    await openSettings();
    const draft = editDraft(initial);
    fireEvent.click(screen.getByRole("button", { name: "新工作" }));
    const modal = within(screen.getByRole("dialog"));
    fireEvent.click(modal.getByRole("checkbox"));
    fireEvent.click(modal.getByRole("button", { name: "開始新工作，保留已保存檔案" }));
    await waitFor(() => expect(bridge.NewWork).toHaveBeenCalledTimes(1));
    await openSettings();
    expectDraft(draft);
    expect(screen.getByText("已生效：實際模式")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "套用本次設定" })).toBeEnabled();
    navigateAwayAndBack();
    expectDraft(draft);
    expect(bridge.SaveSettings).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "放棄未套用草稿" }));
    expect(screen.getAllByLabelText("連線 ID")).toHaveLength(1);
    expect(screen.getByRole("radio", { name: /實際模式/ })).toBeChecked();
    expect(screen.getByRole("button", { name: "套用本次設定" })).toBeDisabled();
    expect(saved()).toEqual(initial.settings);
    expectNoSourceActions(bridge);
  });

  it("preserves but blocks a stale draft when failure resynchronization observes changed saved settings", async () => {
    const { bridge, initial } = installBridge("local");
    render(<App />);
    await openSettings();
    const draft = editDraft(initial);
    const changed = { ...initial.settings, mode: "demo" };
    bridge.SaveSettings.mockRejectedValueOnce(new Error("synthetic uncertain save"));
    bridge.Snapshot.mockResolvedValueOnce({ ...initial, settings: changed });
    fireEvent.click(screen.getByRole("button", { name: "套用本次設定" }));
    await waitFor(() => expect(bridge.Snapshot).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByRole("button", { name: "新工作" })).toBeEnabled());
    expectDraft(draft);
    expect(screen.getByText("已生效：離線")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "套用本次設定" })).toBeDisabled();
    navigateAwayAndBack();
    expectDraft(draft);
    fireEvent.click(screen.getByRole("button", { name: "套用本次設定" }));
    expect(bridge.SaveSettings).toHaveBeenCalledExactlyOnceWith(draft);
    fireEvent.click(screen.getByRole("button", { name: "放棄未套用草稿" }));
    expect(screen.getAllByLabelText("連線 ID")).toHaveLength(1);
    expect(screen.getByRole("radio", { name: /離線/ })).toBeChecked();
    expectNoSourceActions(bridge);
  });

  it("drops an unsaved draft on App remount and never accesses browser storage", async () => {
    const storageGet = vi.spyOn(Storage.prototype, "getItem");
    const storageSet = vi.spyOn(Storage.prototype, "setItem");
    try {
      const { bridge, initial, saved } = installBridge();
      const mounted = render(<App />);
      await openSettings();
      const draft = editDraft(initial);
      expectDraft(draft);
      mounted.unmount();
      render(<App />);
      await openSettings();
      expect(screen.getAllByLabelText("連線 ID")).toHaveLength(1);
      expect(editor(0).getByLabelText("連線 ID")).toHaveValue("original-source");
      expect(editor(0).getByLabelText("本機啟動程式")).toHaveValue("/operator/original-source");
      expect(screen.getByLabelText("模型名稱")).toHaveValue(initial.settings.model);
      expect(screen.getByRole("radio", { name: /離線/ })).toBeChecked();
      expect(screen.getByRole("button", { name: "套用本次設定" })).toBeDisabled();
      expect(saved()).toEqual(initial.settings);
      expect(bridge.SaveSettings).not.toHaveBeenCalled();
      expectNoSourceActions(bridge);
      expect(storageGet).not.toHaveBeenCalled();
      expect(storageSet).not.toHaveBeenCalled();
    } finally {
      storageGet.mockRestore();
      storageSet.mockRestore();
    }
  });
});

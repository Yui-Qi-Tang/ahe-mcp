import React from "react";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import Connections from "./Connections.jsx";
import { emptyState } from "./bridge.js";

function fixture(mode = "local") {
  const state = emptyState();
  state.settings = {
    ...state.settings,
    mode,
    connections: [
      {
        id: "source-1",
        name: "測試來源",
        transport: "stdio",
        command: "/operator/source",
        url: "",
        allowedTools: ["read_status"],
      },
    ],
  };
  return state;
}

function tool(name = "read_status") {
  return {
    connectionID: "source-1",
    name,
    description: "工具描述不等於唯讀權限。",
    digest: "bound-tool-digest",
    schemaSHA256: "schema-digest",
    inventorySHA256: "inventory-digest",
    configSHA256: "config-digest",
    inputSchemaJSON: '{"type":"object"}',
  };
}

// Component tests provide controlled settings; App navigation tests exercise
// the real draft ownership and conflict lifecycle.
function SettingsHarness(props) {
  const fingerprint = JSON.stringify(props.state.settings);
  const [draft, setDraft] = React.useState(props.state.settings);
  React.useEffect(() => setDraft(JSON.parse(fingerprint)), [fingerprint]);
  return (
    <Connections
      {...props}
      draft={draft}
      draftConflict={false}
      onDraftChange={setDraft}
      onDiscardDraft={() => setDraft(props.state.settings)}
    />
  );
}

function setup(state = fixture(), disabled = false) {
  const onAction = vi.fn();
  const onToolConfirm = vi.fn();
  const props = { state, disabled, onAction, onToolConfirm };
  const rendered = render(<SettingsHarness {...props} />);
  return {
    onAction,
    onToolConfirm,
    rerender: (next, busy = false) =>
      rendered.rerender(
        <SettingsHarness {...props} state={next} disabled={busy} />,
      ),
  };
}

function description(element) {
  return element
    .getAttribute("aria-describedby")
    ?.split(" ")
    .map((id) => document.getElementById(id)?.textContent || "")
    .join(" ");
}

describe("Connections settings and manual source workflow", () => {
  it("keeps model advice separate from the existing human call confirmation", async () => {
    const state = fixture();
    state.tools = [tool()];
    const { onAction, onToolConfirm, rerender } = setup(state);
    fireEvent.change(screen.getByLabelText("已套用的伺服器"), { target: { value: "source-1" } });
    fireEvent.change(screen.getByLabelText("工具選擇問題"), { target: { value: "查詢狀態" } });
    const argsJSON = '{\n  "query": "精確原文"\n}\n';
    const result = { ...state, toolAdvice: {
      id: "new-advice", connectionID: "source-1", question: "查詢狀態",
      outcome: "suggested", reason: "請人工核對。", tool: tool(), argumentsJSON: argsJSON,
      model: state.settings.model, createdAt: "2026-09-09T10:00:00Z",
    } };
    onAction.mockImplementationOnce(async () => { rerender(result); return result; });
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "請模型建議工具" })));
    expect(screen.getByLabelText("已探索工具")).toHaveValue("");
    expect(screen.getByLabelText("精確參數 JSON")).toHaveValue("{}");
    expect(onToolConfirm).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "帶入工具與參數草稿" }));
    expect(screen.getByLabelText("已探索工具")).toHaveValue("read_status");
    expect(screen.getByLabelText("精確參數 JSON")).toHaveValue(argsJSON);
    expect(onAction).toHaveBeenCalledExactlyOnceWith("SuggestSourceTool", "source-1", "查詢狀態");
    expect(onToolConfirm).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "檢查並批准這次呼叫" }));
    expect(onToolConfirm).toHaveBeenCalledExactlyOnceWith({ connection: state.settings.connections[0], tool: tool(), argsJSON });
    expect(onAction).toHaveBeenCalledTimes(1);
  });
  it("keeps mode changes as a draft until applied, and saving does not discover or call tools", () => {
    const state = fixture("demo");
    const { onAction, onToolConfirm, rerender } = setup(state);
    const status = within(screen.getByRole("region", { name: "設定套用狀態" }));
    const apply = status.getByRole("button", { name: "套用本次設定" });
    expect(apply).toBeDisabled();
    expect(description(apply)).toMatch(/沒有尚未套用/);
    expect(status.getByText("已生效：離線演練")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("radio", { name: /實際模式/ }));
    expect(status.getByText("已生效：離線演練")).toBeInTheDocument();
    expect(status.getByText(/未套用草稿：實際模式/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "取得工具清單" })).toBeDisabled();
    expect(onAction).not.toHaveBeenCalled();
    fireEvent.click(apply);
    const expected = { ...state.settings, mode: "local" };
    expect(onAction).toHaveBeenCalledExactlyOnceWith("SaveSettings", expected);
    expect(onToolConfirm).not.toHaveBeenCalled();
    expect(status.getByText("已生效：離線演練")).toBeInTheDocument();
    rerender({ ...state, settings: expected });
    expect(status.getByText("已生效：實際模式")).toBeInTheDocument();
    expect(apply).toBeDisabled();
    expect(onAction).toHaveBeenCalledTimes(1);
  });

  it("shows disabled parameters before selection and requires explicit discovery, tool selection and confirmation", () => {
    const state = fixture();
    const { onAction, onToolConfirm, rerender } = setup(state);
    const args = screen.getByLabelText("精確參數 JSON");
    const discover = screen.getByRole("button", { name: "取得工具清單" });
    const approve = screen.getByRole("button", { name: /檢查並批准這次呼叫/ });
    expect(args).toHaveValue("{}");
    expect(args).toBeDisabled();
    expect(description(args)).toMatch(/選擇.*工具/);
    expect(description(discover)).toMatch(/選擇.*伺服器/);
    expect(approve).toBeDisabled();
    fireEvent.change(screen.getByLabelText("已套用的伺服器"), {
      target: { value: "source-1" },
    });
    expect(discover).toBeEnabled();
    expect(onAction).not.toHaveBeenCalled();
    fireEvent.click(discover);
    expect(onAction).toHaveBeenCalledExactlyOnceWith(
      "DiscoverTools",
      "source-1",
    );
    rerender({ ...state, tools: [tool(), tool("write_other")] });
    expect(screen.getByLabelText("已探索工具")).toHaveValue("");
    expect(args).toBeDisabled();
    fireEvent.change(screen.getByLabelText("已探索工具"), {
      target: { value: "read_status" },
    });
    expect(args).toBeEnabled();
    expect(
      args.compareDocumentPosition(screen.getByText("schema-digest")) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    const exact = '{\n  "query": "原文", "instruction": "admit"\n}\n';
    fireEvent.change(args, { target: { value: exact } });
    fireEvent.change(screen.getByLabelText("已探索工具"), {
      target: { value: "write_other" },
    });
    expect(args).toHaveValue(exact);
    expect(approve).toBeDisabled();
    expect(description(approve)).toMatch(/allowlist/);
    fireEvent.change(screen.getByLabelText("已探索工具"), {
      target: { value: "read_status" },
    });
    fireEvent.click(approve);
    expect(onToolConfirm).toHaveBeenCalledExactlyOnceWith({
      connection: state.settings.connections[0],
      tool: tool(),
      argsJSON: exact,
    });
    expect(onAction).toHaveBeenCalledTimes(1);
    fireEvent.click(discover);
    expect(screen.getByLabelText("已探索工具")).toHaveValue("");
    expect(args).toBeDisabled();
    expect(args).toHaveValue(exact);
    expect(onAction).toHaveBeenCalledTimes(2);
    expect(onToolConfirm).toHaveBeenCalledTimes(1);
  });

  it("explains private persisted settings, unsaved chat and offline startup", () => {
    setup();
    expect(
      screen.getByText(/設定會保存於本機私有資料目錄/),
    ).toBeInTheDocument();
    expect(screen.getByText(/聊天不會自動保存/)).toBeInTheDocument();
    expect(screen.getByText(/每次啟動仍從離線演練開始/)).toBeInTheDocument();
    expect(
      screen.queryByText(/只留在這次桌面工作階段/),
    ).not.toBeInTheDocument();
    expect(screen.getByText(/套用只保存設定，不會連線/)).toBeInTheDocument();
  });

  it.each([
    "",
    " ",
    "source one",
    " source-1",
    "source-1 ",
    "source\n1",
    "source-1\n",
    "source-1\r",
    "source-1\u2028",
    "source-1\u2029",
    "來源",
    "x".repeat(129),
  ])(
    "preserves and rejects invalid existing connection ID %j until manually corrected",
    (id) => {
      const state = fixture();
      state.settings.connections[0].id = id;
      const original = structuredClone(state.settings);
      const { onAction } = setup(state);
      const input = screen.getByLabelText("連線 ID");
      const apply = screen.getByRole("button", { name: "套用本次設定" });
      expect(input).toHaveAttribute("aria-invalid", "true");
      expect(description(input)).toMatch(/ASCII|不可空白/);
      expect(document.querySelector(".connection-id-value").textContent).toBe(
        `目前值：${JSON.stringify(id)}`,
      );
      expect(apply).toBeDisabled();
      fireEvent.change(screen.getByLabelText("顯示名稱"), {
        target: { value: "保留原設定並修正" },
      });
      fireEvent.click(apply);
      expect(onAction).not.toHaveBeenCalled();
      expect(state.settings).toEqual(original);
      const corrected = "A_-." + "9".repeat(124);
      fireEvent.change(input, { target: { value: corrected } });
      expect(input).toHaveAttribute("aria-invalid", "false");
      expect(apply).toBeEnabled();
      fireEvent.click(apply);
      const saved = onAction.mock.calls[0][1];
      expect(saved.connections[0]).toEqual({
        ...original.connections[0],
        id: corrected,
        name: "保留原設定並修正",
      });
      expect(onAction).toHaveBeenCalledTimes(1);
    },
  );

  it("marks both duplicate IDs and permits exact case-distinct IDs without normalization", () => {
    const state = fixture();
    state.settings.connections.push({ ...state.settings.connections[0] });
    const { onAction } = setup(state);
    const inputs = screen.getAllByLabelText("連線 ID");
    for (const input of inputs) {
      expect(input).toHaveAttribute("aria-invalid", "true");
      expect(description(input)).toMatch(/重複/);
    }
    const apply = screen.getByRole("button", { name: "套用本次設定" });
    expect(apply).toBeDisabled();
    fireEvent.change(inputs[1], { target: { value: "Source-1" } });
    for (const input of inputs)
      expect(input).toHaveAttribute("aria-invalid", "false");
    fireEvent.click(apply);
    expect(
      onAction.mock.calls[0][1].connections.map((item) => item.id),
    ).toEqual(["source-1", "Source-1"]);
  });

  it("explains busy guards for all buttons and prevents settings or tool operations", () => {
    const state = fixture();
    state.tools = [tool()];
    const { onAction, onToolConfirm, rerender } = setup(state);
    fireEvent.change(screen.getByLabelText("已套用的伺服器"), {
      target: { value: "source-1" },
    });
    fireEvent.change(screen.getByLabelText("已探索工具"), {
      target: { value: "read_status" },
    });
    rerender(state, true);
    for (const button of screen.getAllByRole("button")) {
      expect(button).toBeDisabled();
      expect(description(button)).toMatch(/正在處理|尚未就緒/);
      fireEvent.click(button);
    }
    expect(screen.getByLabelText("精確參數 JSON")).toBeDisabled();
    expect(onAction).not.toHaveBeenCalled();
    expect(onToolConfirm).not.toHaveBeenCalled();
  });

  it("explains unsaved-draft, missing digest and malformed argument guards without changing arguments", () => {
    const state = fixture();
    state.tools = [{ ...tool(), digest: "" }];
    const { onToolConfirm, rerender } = setup(state);
    fireEvent.change(screen.getByLabelText("已套用的伺服器"), {
      target: { value: "source-1" },
    });
    fireEvent.change(screen.getByLabelText("已探索工具"), {
      target: { value: "read_status" },
    });
    const approve = screen.getByRole("button", { name: /檢查並批准這次呼叫/ });
    expect(description(approve)).toMatch(/digest/);
    rerender({ ...state, tools: [tool()] });
    const args = screen.getByLabelText("精確參數 JSON");
    fireEvent.change(args, { target: { value: "[bad" } });
    expect(approve).toBeDisabled();
    expect(description(approve)).toMatch(/JSON/);
    expect(args).toHaveValue("[bad");
    fireEvent.change(args, { target: { value: "{}" } });
    fireEvent.change(screen.getByLabelText("顯示名稱"), {
      target: { value: "新名稱" },
    });
    expect(approve).toBeDisabled();
    expect(description(approve)).toMatch(/未套用/);
    expect(
      description(screen.getByRole("button", { name: "取得工具清單" })),
    ).toMatch(/未套用/);
    fireEvent.click(approve);
    expect(onToolConfirm).not.toHaveBeenCalled();
  });
});

it("requires explicit Atlassian login before discovery and supports local logout", () => {
  const state = fixture();
  state.settings.connections[0] = { ...state.settings.connections[0], transport: "atlassian-oauth", command: "", url: "https://mcp.atlassian.com/v2/mcp" };
  const app = setup(state);
  fireEvent.change(screen.getByLabelText("已套用的伺服器"), { target: { value: "source-1" } });
  expect(screen.getByRole("button", { name: "取得工具清單" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "登入 Atlassian" }));
  expect(app.onAction).toHaveBeenCalledExactlyOnceWith("BeginAtlassianLogin", "source-1");
  const waiting = { ...state, busy: true, sourceAuth: { "source-1": { status: "waiting", authorizationURL: "https://auth.atlassian.com/authorize?state=synthetic" } } };
  app.rerender(waiting, true);
  expect(screen.getByLabelText("Atlassian 授權連結（請複製並自行開啟）")).toHaveValue(waiting.sourceAuth["source-1"].authorizationURL);
  expect(screen.getByRole("button", { name: "登入 Atlassian" })).toBeDisabled();
  app.rerender({ ...state, sourceAuth: { "source-1": { status: "connected" } } });
  expect(screen.queryByLabelText("Atlassian 授權連結（請複製並自行開啟）")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "取得工具清單" })).toBeEnabled();
  fireEvent.click(screen.getByRole("button", { name: "清除本次登入" }));
  expect(app.onAction).toHaveBeenLastCalledWith("DisconnectAtlassian", "source-1");
});

it("restores the pending login link when returning to connections", () => {
  const state = fixture();
  state.settings.connections[0].transport = "atlassian-oauth";
  state.settings.connections[0].command = "";
  state.settings.connections[0].url = "https://mcp.atlassian.com/v2/mcp";
  state.sourceAuth = { "source-1": { status: "waiting", authorizationURL: "https://auth.atlassian.com/authorize?state=synthetic" } };
  setup(state, true);
  expect(screen.getByLabelText("Atlassian 授權連結（請複製並自行開啟）")).toHaveValue(state.sourceAuth["source-1"].authorizationURL);
});

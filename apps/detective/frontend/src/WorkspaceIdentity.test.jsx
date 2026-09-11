import React from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import App from "./App.jsx";
import { emptyState } from "./bridge.js";

afterEach(() => { delete window.go; });

function installWorkspace(extra = {}) {
  const state = { ...emptyState(), workspaceID: "a1b2c3d4e5f6", dataDir: "/private/synthetic/workspace", ...extra };
  const bridge = Object.fromEntries([
    "Snapshot", "SaveSettings", "LoadDemo", "DiscoverTools", "CallSourceTool",
    "SuggestSourceTool", "SendMessage", "Extract", "QueryPending", "SubmitPending",
  ].map((name) => [name, vi.fn(async () => structuredClone(state))]));
  window.go = { main: { App: bridge } };
  return { state, bridge };
}

describe("read-only workspace identity", () => {
  it("does not invent a workspace in a disconnected browser preview", () => {
    delete window.go;
    render(<App />);
    expect(screen.getByLabelText("目前工作區")).toHaveTextContent("工作區尚未確認");
    expect(screen.queryByLabelText("實際資料目錄")).not.toBeInTheDocument();
  });

  it.each([
    { workspaceID: "" }, { dataDir: "" }, { workspaceID: "not-an-id" },
    { dataDir: "relative/workspace" }, { dataDir: 123 },
  ])("does not claim a confirmed identity with missing or invalid coordinates: %j", async (extra) => {
    const { bridge } = installWorkspace(extra);
    render(<App />);
    await waitFor(() => expect(bridge.Snapshot).toHaveBeenCalled());
    expect(screen.getByLabelText("目前工作區")).toHaveTextContent("工作區尚未確認");
  });

  it("waits for the backend snapshot instead of inventing startup coordinates", async () => {
    const { state, bridge } = installWorkspace();
    let resolve;
    bridge.Snapshot.mockImplementationOnce(() => new Promise((done) => { resolve = done; }));
    render(<App />);
    expect(screen.getByLabelText("目前工作區")).toHaveTextContent("工作區尚未確認");
    resolve(state);
    await screen.findByText(/工作區 a1b2c3d4e5f6/);
  });

  it("keeps the backend identity and exact path visible across pages without operations", async () => {
    const { state, bridge } = installWorkspace();
    render(<App />);
    const identity = await screen.findByText(/工作區 a1b2c3d4e5f6/);
    fireEvent.click(identity);
    for (const name of ["資料源與連線", "執行紀錄", "工作台"]) {
      fireEvent.click(screen.getByRole("button", { name, exact: true }));
      expect(screen.getByLabelText("目前工作區")).toHaveTextContent(state.workspaceID);
      expect(screen.getByLabelText("實際資料目錄")).toHaveTextContent(state.dataDir);
    }
    expect(screen.getByLabelText("目前工作區")).toHaveTextContent("不是權限沙箱");
    expect(bridge.Snapshot).toHaveBeenCalledTimes(1);
    for (const [name, method] of Object.entries(bridge)) {
      if (name !== "Snapshot") expect(method).not.toHaveBeenCalled();
    }
  });

  it("renders the full long path as text, not HTML or an editable setting", async () => {
    const path = `/private/${"很長的資料夾 ".repeat(80)}/<img src=x onerror=alert(1)>`;
    installWorkspace({ dataDir: path });
    render(<App />);
    await screen.findByText(/工作區 a1b2c3d4e5f6/);
    const directory = screen.getByLabelText("實際資料目錄");
    expect(directory.textContent).toBe(path);
    expect(directory.querySelector("img")).toBeNull();
    expect(within(screen.getByLabelText("目前工作區")).queryByRole("textbox")).toBeNull();
    expect(directory).toHaveAttribute("dir", "ltr");
  });
});

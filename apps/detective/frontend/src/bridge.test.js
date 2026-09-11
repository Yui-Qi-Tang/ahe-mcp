import { describe, expect, it } from "vitest";
import {
  argumentsError,
  canSubmitPending,
  desktopBridge,
  emptyState,
  normalizeState,
  stateLabel,
  toolConfirmation,
} from "./bridge.js";

describe("desktop bridge guards", () => {
  it("distinguishes completed work from a cancellation request", () => {
    expect(stateLabel("completed")).toBe("已完成");
    expect(stateLabel("cancel_requested")).toBe("已要求取消，尚未確認完成");
    expect(stateLabel("failed")).toBe("失敗");
  });
  it("does not create a demo bridge when the desktop bridge is absent", () => {
    expect(desktopBridge()).toBeNull();
    expect(emptyState().messages).toEqual([]);
    expect(emptyState().settings.baseURL).toBe("http://127.0.0.1:11434/v1");
    expect(emptyState().settings.sourceID).toBe("desktop-status");
    expect(emptyState().version).toBe("0.1.0-preview.18");
    expect(emptyState().search).toBeNull();
  });
  it("normalizes null slices but rejects a missing state envelope", () => {
    expect(() => normalizeState(null)).toThrow();
    const state = normalizeState({
      settings: { mode: "local", connections: null },
      messages: null,
      candidates: null,
      events: null,
      tools: null,
    });
    expect(state.settings.connections).toEqual([]);
    expect(state.messages).toEqual([]);
  });
  it.each(["", "null", "[]", "true", '"object"', "{bad}"])(
    "rejects non-object arguments: %s",
    (text) => {
      expect(argumentsError(text)).not.toBe("");
    },
  );
  it("uses UTF-8 bytes and the 64 KiB contract limit without rewriting valid JSON", () => {
    expect(argumentsError('{"q":"' + "a".repeat(65528) + '"}')).toBe("");
    expect(argumentsError('{"q":"' + "a".repeat(65529) + '"}')).toContain(
      "64 KiB",
    );
    expect(argumentsError('{"q":"' + "臺".repeat(21843) + '"}')).toContain(
      "64 KiB",
    );
    const args = '{\n  "q": "臺灣", "text": "admit"\n}\n';
    expect(argumentsError(args)).toBe("");
    expect(
      toolConfirmation(
        "source-1",
        { name: "read_status", digest: "exact-digest" },
        args,
      ),
    ).toBe("source-1\nread_status\nexact-digest\n" + args);
  });
  it("requires local mode, complete batch identity, both launchers and all 1–16 candidates", () => {
    const state = emptyState();
    state.batchPath = "/private/batch.json";
    state.batchDigest = "exact-batch";
    state.candidates = [{ ordinal: 1 }];
    state.settings.intakeLauncher = "/operator/intake";
    state.settings.queryLauncher = "/operator/query";
    expect(canSubmitPending(state)).toBe(false);
    state.settings.mode = "local";
    expect(canSubmitPending(state)).toBe(true);
    expect(canSubmitPending({ ...state, busy: true })).toBe(false);
    expect(canSubmitPending({ ...state, batchDigest: "" })).toBe(false);
    expect(canSubmitPending({ ...state, candidates: [] })).toBe(false);
    expect(
      canSubmitPending({
        ...state,
        candidates: Array.from({ length: 17 }, () => ({})),
      }),
    ).toBe(false);
    expect(
      canSubmitPending({
        ...state,
        settings: { ...state.settings, queryLauncher: "" },
      }),
    ).toBe(false);
  });
});

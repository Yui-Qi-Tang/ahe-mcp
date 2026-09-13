export const defaultSettings = {
  mode: "demo",
  model: "gemma4:e4b-it-qat",
  baseURL: "http://127.0.0.1:11434/v1",
  sourceID: "desktop-status",
  intakeLauncher: "",
  queryLauncher: "",
  reviewLauncher: "",
  connections: [],
};

export function emptyState() {
  return {
    version: "0.1.0-preview.18",
    busy: false,
    operation: "",
    error: "",
    settings: { ...defaultSettings },
    messages: [],
    events: [],
    source: null,
    candidates: [],
    tools: [],
    toolAdvice: null,
    sourceChat: null,
    batchPath: "",
    batchDigest: "",
    dataDir: "",
    workspaceID: "",
    extraction: null,
    batchResult: null,
    brief: null,
    search: null,
    task: null,
  };
}

export function desktopBridge() {
  const app = window.go?.main?.App;
  return app && typeof app.Snapshot === "function" ? app : null;
}

export function normalizeState(state) {
  if (!state || typeof state !== "object" || !state.settings) {
    throw new Error("Invalid desktop state");
  }
  return {
    ...emptyState(),
    ...state,
    settings: {
      ...defaultSettings,
      ...state.settings,
      connections: state.settings.connections || [],
    },
    messages: state.messages || [],
    events: state.events || [],
    candidates: state.candidates || [],
    tools: state.tools || [],
  };
}

export function argumentsError(text) {
  try {
    const value = JSON.parse(text);
    if (!value || typeof value !== "object" || Array.isArray(value))
      return "參數必須是 JSON 物件。";
    if (new TextEncoder().encode(text).length > 64 * 1024)
      return "參數超過 64 KiB 限制。";
    return "";
  } catch {
    return "請輸入有效的 JSON 物件；不會自動修補或改寫參數。";
  }
}

export function toolConfirmation(connectionID, tool, argsJSON) {
  return `${connectionID}\n${tool.name}\n${tool.digest}\n${argsJSON}`;
}

export function canSubmitPending(state) {
  return (
    state.settings.mode === "local" &&
    !state.task &&
    !state.busy &&
    Boolean(
      state.batchDigest &&
        state.batchPath &&
        state.settings.intakeLauncher &&
        state.settings.queryLauncher,
    ) &&
    state.candidates.length >= 1 &&
    state.candidates.length <= 16
  );
}

export function stateLabel(value) {
  return (
    {
      pending: "待處理",
      pending_verified: "已查回 pending",
      admitted_verified: "已查回採納狀態",
      admit_verified: "已查回採納狀態",
      rejected_verified: "已查回拒絕狀態",
      reject_verified: "已查回拒絕狀態",
      admitted: "已查回採納狀態",
      rejected: "已查回拒絕狀態",
      audit_only: "已查回稽核保留",
      inspected: "已核對輸入，尚未呼叫模型",
      model_requested: "模型輸出尚未確認完成",
      extraction_failed: "模型閱讀未完成，保留原次結果",
      candidate_saved: "本機候選已保存，尚未送出",
      submission_started: "提交已開始，結果尚未確認",
      submitted_unverified: "已保存提交收據，尚未查回",
      review_ready: "精確審查已保存，尚未決定",
      decision_saved: "決定已保存，寫入結果尚未確認",
      decision_acknowledged: "處置已回覆，尚未精確查回",
      audit_only_verified: "已查回稽核保留",
      not_attempted: "尚未執行",
      not_checked: "尚未查詢",
      failed: "失敗",
      incomplete: "尚未完成",
      complete: "已完成",
      candidate: "本機候選",
      ready: "就緒",
      running: "執行中",
      completed: "已完成",
      cancel_requested: "已要求取消，尚未確認完成",
      success: "完成",
      error: "失敗",
      cancelled: "已取消",
      demo: "離線示範",
    }[value] ||
    value ||
    "尚無狀態"
  );
}

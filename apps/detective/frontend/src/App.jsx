import React, { useCallback, useEffect, useRef, useState } from "react";
import Connections from "./Connections.jsx";
import CodebaseChat, { codebaseConnections } from "./CodebaseChat.jsx";
import BriefWorkspace from "./BriefWorkspace.jsx";
import EvidenceSearch, { initialSearchDraft, sameSearchRequest, searchDraftError } from "./EvidenceSearch.jsx";
import {
  canSubmitPending,
  desktopBridge,
  emptyState,
  normalizeState,
  stateLabel,
  toolConfirmation,
} from "./bridge.js";
import {
  Badge,
  BatchDetails,
  CandidateCard,
  Code,
  Dialog,
  Empty,
  ExtractionDetails,
  Icon,
  KeyValue,
} from "./components.jsx";

function SourceCaptureDetails({ capture }) {
  if (!capture) return null;
  const inspection = capture.inspection;
  return (
    <details aria-label="已保存來源收據檢視">
      <summary>已保存的來源收據與離線核對</summary>
      <p className="notice">
        {inspection?.verifiedAt
          ? "檔案與已保存收據一致"
          : "這份來源尚無離線重新核對紀錄。"}
      </p>
      <p className="field-help">
        僅核對保存檔案的一致性，不證明來源真實性、人工核准、最新 DB 狀態或
        canonical 採納。 核對結果僅代表下列時間點。
        以下是保存當時的資料，不會套用為目前設定、工具權限或指令；沒有重新連線來源。
      </p>
      <dl>
        {capture.codeCitation && <>
          <KeyValue label="核對的本機檔案">{capture.codeCitation.filePath}:{capture.codeCitation.startLine}–{capture.codeCitation.endLine}</KeyValue>
          <KeyValue label="觀測內容版本 SHA-256（非 Git revision）">{capture.codeCitation.fileSHA256}</KeyValue>
        </>}
        <KeyValue label="來源取得時間">{capture.capturedAt}</KeyValue>
        <KeyValue label="來源 revision">
          {capture.revision || "unknown"}
        </KeyValue>
        <KeyValue label="最近一次離線核對時間">
          {inspection?.verifiedAt || "尚未核對"}
        </KeyValue>
        <KeyValue label="已保存收據路徑">{capture.receipt?.path}</KeyValue>
        <KeyValue label="收據 SHA-256">{capture.receipt?.sha256}</KeyValue>
        <KeyValue label="收據 bytes">{capture.receipt?.bytes}</KeyValue>
        <KeyValue label="原始結果路徑">{capture.rawResult?.path}</KeyValue>
        <KeyValue label="原始結果 SHA-256">
          {capture.rawResult?.sha256}
        </KeyValue>
        <KeyValue label="原始結果 bytes">{capture.rawResult?.bytes}</KeyValue>
      </dl>
      {capture.codeCitation && <Code label="已核對的程式原文">{capture.codeCitation.exactQuote}</Code>}
      {inspection && (
        <>
          <h4 className="section-label">完整保存連線設定</h4>
          <Code label="完整保存連線設定">
            {JSON.stringify(inspection.config, null, 2)}
          </Code>
          <h4 className="section-label">完整保存工具契約</h4>
          <Code label="完整保存工具契約">
            {JSON.stringify(inspection.tool, null, 2)}
          </Code>
          <h4 className="section-label">已保存精確工具參數</h4>
          <Code label="已保存精確工具參數">{inspection.argumentsJSON}</Code>
          <details>
            <summary>完整原始工具結果 JSON</summary>
            <Code label="已保存原始工具結果">{inspection.rawResultJSON}</Code>
          </details>
        </>
      )}
    </details>
  );
}

function WorkspaceIdentity({ state }) {
  const confirmed = typeof state.dataDir === "string" && state.dataDir.startsWith("/") &&
    typeof state.workspaceID === "string" && /^[a-f0-9]{12}$/.test(state.workspaceID);
  return (
    <section className="workspace-identity" aria-label="目前工作區">
      {confirmed ? (
        <details>
          <summary>工作區 {state.workspaceID} · 查看資料目錄</summary>
          <p>本視窗實際使用的資料目錄（唯讀）</p>
          <code className="workspace-path" aria-label="實際資料目錄" dir="ltr">{state.dataDir}</code>
          <p>短識別只協助區分路徑，不代表測試／正式環境，也不是權限沙箱。請核對完整目錄；同一目錄不要同時開啟兩個程序。</p>
        </details>
      ) : <span>工作區尚未確認 · 請從原生桌面程式取得目前資料目錄。</span>}
    </section>
  );
}

export default function App() {
  const [state, setState] = useState(emptyState);
  // Drafts survive page unmounts but never enter backend/model state or storage.
  const [settingsEdit, setSettingsEdit] = useState(null);
  const [briefDraft, setBriefDraft] = useState(null);
  const [searchDraft, setSearchDraft] = useState(initialSearchDraft);
  const [searchFeedback, setSearchFeedback] = useState(null);
  const [connected, setConnected] = useState(false);
  const [initializing, setInitializing] = useState(Boolean(desktopBridge()));
  const [working, setWorking] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [error, setError] = useState("");
  const [toolFeedback, setToolFeedback] = useState(null);
  const [page, setPage] = useState("desk");
  const [chatConnectionID, setChatConnectionID] = useState("");
  const [pane, setPane] = useState("source");
  const [message, setMessage] = useState("");
  const [line, setLine] = useState("");
  const [confirmation, setConfirmation] = useState(null);
  const locked = useRef(false);
  const cancelLocked = useRef(false);
  const sequence = useRef(0);
  const applied = useRef(0);
  const busy = working || state.busy || cancelling;
  const disabled = !connected || busy;
  const demo = state.settings.mode === "demo";
  const legacyWork = !state.task && Boolean(state.batchPath || state.batchDigest || state.candidates.length || state.extraction);
  const settingsFingerprint = JSON.stringify(state.settings);
  const settingsDraft = settingsEdit?.draft ?? state.settings;
  const settingsChanged = JSON.stringify(settingsDraft) !== settingsFingerprint;
  const settingsConflict = Boolean(
    settingsChanged && settingsEdit?.base !== settingsFingerprint,
  );
  const searchBlockedReason = !connected
    ? "需從原生桌面程式啟動並完成狀態同步。"
    : busy ? "正在處理操作；請等待完成或取消後再查詢。"
      : typeof desktopBridge()?.SearchEvidence !== "function" ? "目前桌面版本尚未提供證據搜尋，請更新桌面程式。"
        : settingsChanged ? "有尚未套用的設定草稿；請先前往連線設定套用或捨棄。"
          : state.settings.mode !== "local" ? "搜尋需切換為實際模式並套用設定。"
            : !state.settings.queryLauncher ? "請先在資料源與連線的進階連線設定填入證據查詢程式。搜尋不需要模型，也不需要啟用寫入。" : "";
  const accept = useCallback((value, ticket) => {
    const normalized = normalizeState(value);
    if (ticket >= applied.current) {
      applied.current = ticket;
      setState(normalized);
    }
    setConnected(true);
    return normalized;
  }, []);
  const snapshot = useCallback(async () => {
    const bridge = desktopBridge();
    if (!bridge) {
      setConnected(false);
      setInitializing(false);
      return null;
    }
    const ticket = ++sequence.current;
    try {
      return accept(await bridge.Snapshot(), ticket);
    } catch {
      setError("無法同步桌面狀態。保留目前檢視內容，請稍後重新同步。");
      return null;
    } finally {
      setInitializing(false);
    }
  }, [accept]);
  useEffect(() => {
    void snapshot();
  }, [snapshot]);
  useEffect(() => {
    if (!busy) return undefined;
    let polling = false;
    const interval = setInterval(async () => {
      if (polling) return;
      polling = true;
      try {
        await snapshot();
      } finally {
        polling = false;
      }
    }, 1000);
    return () => clearInterval(interval);
  }, [busy, snapshot]);
  useEffect(() => {
    setLine("");
  }, [state.source?.sha256, state.source?.id]);
  useEffect(() => {
    if (!legacyWork) setPane("source");
  }, [legacyWork]);
  useEffect(() => {
    if (!busy) setSearchFeedback((current) => current?.status === "cancel_requested" ? { ...current, status: "cancelled" } : current);
  }, [busy]);

  function updateSettingsDraft(update) {
    setSettingsEdit((current) => {
      const draft = update(current?.draft ?? state.settings);
      if (JSON.stringify(draft) === settingsFingerprint) return null;
      const base = current && JSON.stringify(current.draft) !== settingsFingerprint
        ? current.base
        : settingsFingerprint;
      return { base, draft };
    });
  }

  async function action(method, ...args) {
    if (locked.current || state.busy) return null;
    const bridge = desktopBridge();
    if (!bridge || typeof bridge[method] !== "function") {
      setConnected(false);
      setError("需從桌面程式啟動；此頁不會模擬操作成功。");
      return null;
    }
    locked.current = true;
    setWorking(true);
    setError("");
    setToolFeedback(null);
    function reconcileBriefDraft(accepted) {
      if (!accepted) return;
      if (method === "SaveSettings" && ["mode", "intakeLauncher", "queryLauncher", "reviewLauncher"].some((key) =>
        accepted.settings[key] !== state.settings[key])) {
        setBriefDraft((current) => current ? { ...current, review: null } : null);
      }
      if (!accepted.brief) setBriefDraft(null);
    }
    try {
      const result = await bridge[method](...args);
      const accepted = accept(result, ++sequence.current);
      reconcileBriefDraft(accepted);
      if (method === "SaveSettings") {
        // A successful explicit save adopts backend-normalized settings. A
        // rejected/uncertain save preserves the draft; never retry it here.
        setSettingsEdit((current) => current === settingsEdit ? null : current);
      }
      if ((method === "SaveSettings" || method === "NewWork") && !accepted.error) setSearchFeedback(null);
      return accepted;
    } catch {
      setError(
        method === "SearchEvidence"
          ? "唯讀搜尋未完成。正在嘗試同步狀態；不能判定為查無資料，請檢查執行紀錄。不會自動重試或寫入 DB。"
          : "操作未完成。已重新同步桌面狀態，請檢查保留的來源、逐候選結果與執行紀錄；不會自動重試。",
      );
      reconcileBriefDraft(await snapshot());
      return null;
    } finally {
      locked.current = false;
      setWorking(false);
    }
  }
  async function runEvidenceSearch(request) {
    if (locked.current || searchBlockedReason || searchDraftError(searchDraft)) return;
    setSearchFeedback({ status: "running", request });
    const result = await action("SearchEvidence", request);
    const confirmed = result?.search && result.search.demo === false && !result.error && !result.busy && sameSearchRequest(result.search.request, request);
    setSearchFeedback((current) => ({ status: current?.status === "cancel_requested" ? "cancelled" : confirmed ? "complete" : "unconfirmed", request }));
  }
  async function cancel() {
    const bridge = desktopBridge();
    if (!bridge?.Cancel || cancelLocked.current) return;
    cancelLocked.current = true;
    setCancelling(true);
    const searchOperation = searchFeedback?.status === "running" || searchFeedback?.status === "cancel_requested" || state.operation === "evidence_search";
    if (searchOperation) setSearchFeedback((current) => ({ ...current, status: "cancel_requested" }));
    try {
      accept(await bridge.Cancel(), ++sequence.current);
    } catch {
      setError(searchOperation ? "唯讀搜尋的取消請求未確認。請檢查執行紀錄；不會自動重試或寫入 DB。" : "取消請求未確認。取消不代表寫入回復，請檢查逐候選狀態。");
      await snapshot();
    } finally {
      cancelLocked.current = false;
      setCancelling(false);
    }
  }
  const closeConfirmation = useCallback(() => setConfirmation(null), []);
  function requestPending() {
    if (disabled || !canSubmitPending(state)) return;
    setConfirmation({
      kind: "pending",
      digest: state.batchDigest,
      count: state.candidates.length,
      path: state.batchPath,
      intake: state.settings.intakeLauncher,
      query: state.settings.queryLauncher,
      candidates: JSON.parse(JSON.stringify(state.candidates)),
    });
  }
  function requestTool(value) {
    setConfirmation({
      kind: "tool",
      ...value,
      connection: {
        ...value.connection,
        allowedTools: [...value.connection.allowedTools],
      },
      tool: { ...value.tool },
    });
  }
  async function confirm() {
    const intent = confirmation;
    setConfirmation(null);
    if (!intent || disabled) return;
    if (intent.kind === "reset") {
      const result = await action("NewWork");
      if (result && !result.error) {
        setPage("desk");
        setPane("source");
        setMessage("");
        setBriefDraft(null);
        setSearchDraft(initialSearchDraft());
      }
      return;
    }
    if (intent.kind === "pending") {
      if (
        !canSubmitPending(state) ||
        intent.digest !== state.batchDigest ||
        intent.count !== state.candidates.length ||
        intent.path !== state.batchPath ||
        intent.intake !== state.settings.intakeLauncher ||
        intent.query !== state.settings.queryLauncher
      ) {
        setError(
          "批次或連線程式已變動，先前確認已失效。請重新核對完整候選。",
        );
        return;
      }
      await action("SubmitPending", intent.digest);
      setPane("candidates");
    } else {
      const connection = state.settings.connections.find(
        (item) => item.id === intent.connection.id,
      );
      const tool = state.tools.find(
        (item) =>
          item.connectionID === intent.connection.id &&
          item.name === intent.tool.name,
      );
      if (
        demo ||
        JSON.stringify(connection) !== JSON.stringify(intent.connection) ||
        tool?.digest !== intent.tool.digest ||
        !connection?.allowedTools?.includes(tool.name)
      ) {
        setError(
          "連線、allowlist 或工具契約已變動，這次確認已失效。請重新探索工具並確認。",
        );
        return;
      }
      const previousSourcePath = state.source?.path;
      const previousEventIDs = new Set(state.events.map((event) => event.id));
      const result = await action(
        "CallSourceTool",
        connection.id,
        tool.name,
        intent.argsJSON,
        toolConfirmation(connection.id, tool, intent.argsJSON),
      );
      if (result) {
        const completion = result.events.at(-1);
        // Only this call's completed source capture may be presented as new.
        // A retained source after cancellation/partial failure is not success.
        // Source IDs are content hashes; unchanged bytes can have a new path.
        const completed =
          !result.busy &&
          !result.error &&
          completion?.action === "來源工具讀取" &&
          completion.status === "completed" &&
          !previousEventIDs.has(completion.id) &&
          result.source?.kind === "mcp" &&
          Boolean(result.source.path) &&
          result.source.path !== previousSourcePath;
        setToolFeedback({
          completed,
          sourcePath: completed ? result.source.path : null,
          toolName: tool.name,
        });
      }
    }
  }
  async function send(event) {
    event.preventDefault();
    if (!message.trim() || disabled || demo || settingsChanged) return;
    const sent = message;
    if (chatConnectionID && !codebaseConnections(state).some((c) => c.id === chatConnectionID)) {
      setChatConnectionID(""); setError("讀取連線已變更，請重新選擇。"); return;
    }
    const result = chatConnectionID
      ? await action("StartSourceChat", chatConnectionID, sent)
      : await action("SendMessage", sent);
    if (result) setMessage((current) => (current === sent ? "" : current));
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark">
            <Icon name="search" size={25} />
          </span>
          <div>
            <strong>Detective</strong>
            <small>來源與證據工作桌</small>
          </div>
        </div>
        <div className="workspace-name">
          <span className="workspace-monogram">D</span>
          <div>
            個人工作空間<small>本機 · Desktop preview</small>
          </div>
        </div>
        <p className="nav-label">工作空間</p>
        <nav aria-label="主要導覽">
          {[
            ["desk", "工作台"],
            ["search", "證據搜尋"],
            ["source", "資料源與連線"],
            ["activity", "執行紀錄"],
          ].map(([id, title]) => (
            <button
              key={id}
              className={`nav-button ${page === id ? "active" : ""}`}
              aria-current={page === id ? "page" : undefined}
              onClick={() => setPage(id)}
            >
              <Icon name={id} />
              {title}
              {id === "activity" && state.events.length > 0 && (
                <span className="nav-count">{state.events.length}</span>
              )}
            </button>
          ))}
        </nav>
        <div className="sidebar-note">
          <Icon name="shield" />
          <strong>先看清楚，再決定</strong>
          <p>
            來源、候選與資料庫狀態分開呈現。聊天不是核准，pending 不是採納。
          </p>
        </div>
        <div className="sidebar-footer">
          <span className={`status-dot ${demo ? "offline" : ""}`} />
          <span>{demo ? "離線 · 尚未啟用連線" : "明確實際模式"}</span>
          <small>{state.version}</small>
        </div>
      </aside>
      <div className="main-shell">
        <header className="topbar">
          <div className="breadcrumbs">
            Detective <span>/</span>{" "}
            {page === "desk"
              ? "工作台"
              : page === "search" ? "證據搜尋" : page === "source"
                ? "資料源與連線"
                : "執行紀錄"}
          </div>
          <div className="topbar-actions">
            <button
              className="button secondary compact"
              disabled={disabled || typeof desktopBridge()?.NewWork !== "function"}
              onClick={() => setConfirmation({ kind: "reset" })}
            >
              <Icon name="add" size={15} />
              新工作
            </button>
            <Badge tone={demo ? "neutral" : "teal"}>
              {demo ? "離線 · 不連模型與 DB" : "實際 · 明確操作"}
            </Badge>
            {busy ? (
              <button
                className="button cancel-button"
                onClick={cancel}
                disabled={!connected || cancelling}
              >
                {cancelling ? "正在取消…" : "取消操作"}
              </button>
            ) : (
              <button
                className="icon-button"
                onClick={() => {
                  void snapshot();
                }}
                title="重新同步"
                aria-label="重新同步桌面狀態"
              >
                <Icon name="activity" size={18} />
              </button>
            )}
          </div>
        </header>
        <WorkspaceIdentity state={state} />
        {initializing && (
          <div className="working-bar" role="status">
            <span className="spinner" />
            正在取得桌面工作狀態；同步完成前不開放操作。
          </div>
        )}
        {!connected && !initializing && (
          <div className="global-alert" role="alert">
            <Icon name="shield" size={18} />
            <span>
              <strong>需從桌面程式啟動</strong>　目前沒有可用的桌面
              bridge；這個瀏覽器檢視不會模擬模型、工具或資料庫成功。
            </span>
          </div>
        )}
        {(error || state.error) && (
          <div className="global-alert error" role="alert">
            <Icon name="activity" size={18} />
            <div>
              {error && <p>{error}</p>}
              {state.error && <p>{state.error}</p>}
              <button
                className="text-button"
                onClick={() => setPage("activity")}
              >
                查看執行紀錄
              </button>
            </div>
            <button
              className="icon-button"
              aria-label="關閉本機錯誤訊息"
              onClick={() => setError("")}
              disabled={!error}
            >
              <Icon name="close" size={16} />
            </button>
          </div>
        )}
        {toolFeedback && (
          <div
            className={`tool-feedback ${toolFeedback.completed ? "completed" : "unconfirmed"}`}
            role="status"
          >
            <div>
              <strong>
                {toolFeedback.completed
                  ? "來源工具已完成"
                  : "來源工具未確認完成"}
              </strong>
              <p>
                {toolFeedback.completed
                  ? `${toolFeedback.toolName} 的回覆已保存；尚未抽取、送入 pending 或採納。`
                  : "目前保留的來源不代表這次成功。請查看執行紀錄，不會自動重試。"}
              </p>
            </div>
            <div className="tool-feedback-actions">
              {toolFeedback.completed && (
                <button
                  className="button secondary compact"
                  disabled={state.source?.path !== toolFeedback.sourcePath}
                  onClick={() => {
                    setPage("desk");
                    setPane("source");
                  }}
                >
                  查看本次來源
                </button>
              )}
              <button
                className="text-button"
                onClick={() => setPage("activity")}
              >
                查看執行紀錄
              </button>
              <button
                className="icon-button"
                aria-label="關閉工具結果提示"
                onClick={() => setToolFeedback(null)}
              >
                <Icon name="close" size={16} />
              </button>
            </div>
          </div>
        )}
        {busy && (
          <div className="working-bar" role="status">
            <span className="spinner" />
            {state.operation || "正在處理明確操作"}
            <span>每秒同步 · 不自動重試</span>
          </div>
        )}
        {page === "search" ? (
          <EvidenceSearch state={state} draft={searchDraft} onDraftChange={setSearchDraft}
            blockedReason={searchBlockedReason}
            onSearch={runEvidenceSearch} onSettings={() => setPage("source")} feedback={searchFeedback} />
        ) : page === "source" ? (
          <div className="page-scroll">
            <Connections
              state={state}
              draft={settingsDraft}
              draftConflict={settingsConflict}
              onDraftChange={updateSettingsDraft}
              onDiscardDraft={() => setSettingsEdit(null)}
              disabled={disabled}
              onAction={action}
              onToolConfirm={requestTool}
            />
          </div>
        ) : page === "activity" ? (
          <div className="page-scroll">
            <div className="activity-page">
              <div className="page-intro">
                <p className="eyebrow">ACTIVITY</p>
                <h1>執行紀錄</h1>
                <p>
                  區分操作結果與權限效果。失敗、取消與部分完成都保留，不以單一成功訊息取代逐候選狀態。
                </p>
              </div>
              <section className="surface activity-list">
                {state.events.length === 0 ? (
                  <Empty title="還沒有執行紀錄" icon="activity">
                    完成一項明確操作後，這裡會顯示後端回傳的執行摘要。
                  </Empty>
                ) : (
                  state.events.map((event) => (
                    <article className="event" key={event.id}>
                      <span
                        className={`event-dot ${event.status === "failed" || event.status === "error" ? "failed" : ""}`}
                      />
                      <div>
                        <div className="section-heading">
                          <h3>{event.action}</h3>
                          <Badge>{stateLabel(event.status)}</Badge>
                        </div>
                        <p>{event.detail}</p>
                        <time>{event.time}</time>
                      </div>
                    </article>
                  ))
                )}
              </section>
              <BatchDetails result={state.batchResult} />
            </div>
          </div>
        ) : !state.task && state.brief ? (
          <BriefWorkspace state={state} disabled={disabled} onAction={action} draft={briefDraft} onDraftChange={setBriefDraft} />
        ) : (
          <div className="workspace-grid">
            <section className="conversation">
              <header className="conversation-header">
                <div>
                  <p className="eyebrow">YOUR INVESTIGATION DESK</p>
                  <h1>把來源，變成可追問的線索。</h1>
                  <p>先理解原文與範圍，再逐筆檢查候選。</p>
                </div>
              </header>
              <div className="messages" aria-live="polite" aria-busy={busy}>
                {state.messages.length === 0 ? (
                  <div className="welcome">
                    <span className="welcome-symbol">
                      <Icon name="search" size={34} />
                    </span>
                    <p className="eyebrow">從一份來源開始</p>
                    <h2>你好，我是 Detective。</h2>
                    <p>
                      我會協助你整理來源、查看候選與追問理由。
                      <br />
                      所有結果都保留範圍，所有寫入都需要另外確認。
                    </p>
                    <div className="welcome-cards">
                      <button
                        disabled={disabled}
                        onClick={() => setPage("source")}
                      >
                        <Icon name="source" />
                        <strong>連接資料源</strong>
                        <span>
                          設定來源 MCP 與本機模型
                          <br />
                          確認工具與參數後取得來源
                        </span>
                        <Icon name="arrow" size={16} />
                      </button>
                    </div>
                    <div className="welcome-boundary">
                      <Icon name="shield" size={16} />
                      聊天不會自行呼叫工具或寫入 DB。
                    </div>
                  </div>
                ) : (
                  state.messages.map((item) => (
                    <article
                      className={`message ${item.role === "user" ? "user" : "assistant"}`}
                      key={item.id}
                    >
                      <div className="message-label">
                        <span>{item.role === "user" ? "你" : "Detective"}</span>
                        <Badge tone="neutral">
                          {item.kind === "demo"
                            ? "離線固定提示 · 非模型判斷"
                            : item.kind || "後端訊息"}
                        </Badge>
                        <time>{item.time}</time>
                      </div>
                      <div className="message-text">{item.text}</div>
                    </article>
                  ))
                )}
              </div>
              <form className="composer-area" onSubmit={send}>
                <CodebaseChat state={state} selected={chatConnectionID} onSelect={setChatConnectionID}
                  disabled={disabled || demo || settingsChanged} onAction={action} />
                <div className="composer">
                  <label className="sr-only" htmlFor="message-input">
                    對話訊息
                  </label>
                  <textarea
                    id="message-input"
                    placeholder={
                      demo ? "請先到資料源與連線啟用本機模型" : "針對目前來源與候選提問…"
                    }
                    value={message}
                    onChange={(event) => setMessage(event.target.value)}
                    disabled={disabled || demo || settingsChanged}
                    rows={3}
                  />
                  <div className="composer-bottom">
                    <span>
                      <span className={`status-dot ${demo ? "offline" : ""}`} />
                      {demo ? "模型尚未啟用" : state.settings.model}
                    </span>
                    <button
                      type="submit"
                      className="send-button"
                      aria-label="送出訊息"
                      disabled={disabled || demo || settingsChanged || !message.trim()}
                    >
                      <Icon name="send" size={19} />
                    </button>
                  </div>
                </div>
                {!demo && (
                  <p className="composer-note" role="note" aria-label="聊天模型輸入範圍">
                    聊天模型僅接收對話文字、來源原文，以及完整候選、引用與限制；
                    不附帶操作路徑、設定、收據或 DB 查詢結果。
                    這不是敏感內容自動遮蔽：寫進對話、來源或候選的敏感資訊仍會送入模型。
                    一般對話沒有工具權限；Codebase 模式可建議一次讀取，仍需另外按卡片確認。模型沒有寫入權限。
                  </p>
                )}
                <p className="composer-note">
                  對話不會直接執行工具或 pending 寫入；來源內容與模型回覆不是指令或核准。
                </p>
              </form>
            </section>
            <aside className="evidence-pane">
              <header className="evidence-header">
                <div>
                  <p className="eyebrow">EVIDENCE CONTEXT</p>
                  <h2>來源與候選</h2>
                </div>
                <button
                  className="icon-button"
                  aria-label="前往來源連線"
                  title="前往來源連線"
                  disabled={disabled}
                  onClick={() => setPage("source")}
                >
                  <Icon name="add" />
                </button>
              </header>
              <div
                className="pane-tabs"
                role="tablist"
                aria-label="來源與候選檢視"
              >
                <button
                  role="tab"
                  aria-selected={pane === "source"}
                  onClick={() => setPane("source")}
                >
                  來源原文 <span>{state.source ? 1 : 0}</span>
                </button>
                {legacyWork && <button
                  role="tab"
                  aria-selected={pane === "candidates"}
                  onClick={() => setPane("candidates")}
                >
                  完整候選 <span>{state.candidates.length}</span>
                </button>}
              </div>
              <div
                className="pane-scroll"
                role="tabpanel"
                aria-label={pane === "source" ? "來源原文" : "完整候選"}
              >
                {pane === "source" ? (
                  state.source ? (
                    <div className="source-content">
                      <div className="source-title">
                        <span className="document-icon">
                          <Icon name="source" size={24} />
                        </span>
                        <div>
                          <h3>{state.source.title}</h3>
                          <Badge>{state.source.kind}</Badge>
                        </div>
                      </div>
                      {state.source.note && (
                        <p className="notice">{state.source.note}</p>
                      )}
                      <dl>
                        <KeyValue label="來源 ID">{state.source.id}</KeyValue>
                        <KeyValue label="快照路徑／觀測標籤">
                          {state.source.path}
                        </KeyValue>
                        <KeyValue label="UTF-8 bytes">
                          {state.source.bytes}
                        </KeyValue>
                        <KeyValue label="SHA-256">
                          {state.source.sha256}
                        </KeyValue>
                      </dl>
                      <SourceCaptureDetails capture={state.source.capture} />
                      {legacyWork && <div className="extract-controls">
                        <label>
                          選取來源資料列
                          <select
                            aria-label="選取來源資料列"
                            value={line}
                            disabled={
                              disabled || !(state.source.rows || []).length
                            }
                            onChange={(event) => setLine(event.target.value)}
                          >
                            <option value="">請選擇精確資料列</option>
                            {(state.source.rows || []).map((row) => (
                              <option key={row.line} value={row.line}>
                                第 {row.line} 行 · {row.text}
                              </option>
                            ))}
                          </select>
                        </label>
                        <button
                          className="button primary full-width"
                          disabled={
                            disabled ||
                            !line ||
                            !(state.source.rows || []).some(
                              (row) => String(row.line) === line,
                            )
                          }
                          onClick={async () => {
                            await action("Extract", Number(line));
                            setPane("candidates");
                          }}
                        >
                          抽取此列候選
                          <Icon name="arrow" size={16} />
                        </button>
                        <p className="field-help">
                          僅處理後端辨識的 STATUS
                          資料列；不會將一般文字轉成候選或送入 DB。
                        </p>
                      </div>}
                      <h4 className="section-label">完整來源原文</h4>
                      <Code label="完整來源原文">{state.source.rawText}</Code>
                      <details>
                        <summary>完整來源座標</summary>
                        <Code>{JSON.stringify(state.source, null, 2)}</Code>
                      </details>
                    </div>
                  ) : (
                    <Empty title="來源會留在這裡">
                      到資料源與連線確認工具與參數，取得來源。原文不會被候選摘要取代。
                    </Empty>
                  )
                ) : (
                  <>
                    <div className="candidate-boundary">
                      <Icon name="shield" size={17} />
                      <p>
                        完整候選 {state.candidates.length}{" "}
                        筆。來源支持不等於語意真實；來源狀態與資料庫處理狀態分開呈現。
                      </p>
                    </div>
                    {state.candidates.length === 0 ? (
                      <Empty title="尚無候選">
                        選取可處理的來源列後抽取；即使抽取失敗，已回傳的結果與限制仍會保留。
                      </Empty>
                    ) : (
                      state.candidates.map((candidate) => (
                        <CandidateCard
                          key={candidate.ordinal}
                          candidate={candidate}
                        />
                      ))
                    )}
                    <ExtractionDetails extraction={state.extraction} />
                    <BatchDetails result={state.batchResult} />
                    <dl className="batch-coordinate">
                      <KeyValue label="完整批次 digest">
                        {state.batchDigest}
                      </KeyValue>
                      <KeyValue label="批次 index 路徑">
                        {state.batchPath}
                      </KeyValue>
                    </dl>
                  </>
                )}
              </div>
              {legacyWork && <footer className="evidence-footer">
                <button
                  className="button secondary"
                  disabled={
                    disabled ||
                    Boolean(state.task) ||
                    demo ||
                    !state.batchPath ||
                    !state.settings.queryLauncher
                  }
                  onClick={async () => {
                    await action("QueryPending");
                    setPane("candidates");
                  }}
                >
                  查詢 pending
                </button>
                <button
                  className="button primary"
                  disabled={disabled || !canSubmitPending(state)}
                  onClick={requestPending}
                >
                  檢查並送 pending
                </button>
                <p>
                  pending 會寫入 DB，不是 canonical 採納。
                  <br />
                  需獨立確認全部候選，以及待審提交與證據查詢程式。
                </p>
              </footer>}
            </aside>
          </div>
        )}
      </div>
      {confirmation && (
        <Dialog
          title={
            confirmation.kind === "reset"
              ? "開始新工作"
              : confirmation.kind === "pending"
                ? "確認完整批次的 pending 寫入"
                : "確認這一次來源工具呼叫"
          }
          onClose={closeConfirmation}
          onConfirm={confirm}
          confirmLabel={
            confirmation.kind === "reset"
              ? "開始新工作，保留已保存檔案"
              : confirmation.kind === "pending"
                ? "同意這次 pending 寫入"
                : "同意這次工具呼叫"
          }
          disabled={disabled}
        >
          {confirmation.kind === "reset" ? (
            <>
              <div className="notice">
                只清除目前來源、任務、候選、搜尋與對話檢視。先前保存的 checkpoint、批次與收據不會刪除。
              </div>
              <p>
                保留已套用設定、來源登入及工具清單；不連線、不呼叫模型，也不寫入 DB。未套用的設定草稿仍保留。
              </p>
            </>
          ) : confirmation.kind === "pending" ? (
            <>
              <div className="notice warning">
                這會透過指定 intake 寫入
                pending，不是採納；取消或逾時也不保證已發生的寫入回復。
              </div>
              <dl>
                <KeyValue label="全部候選數">{confirmation.count}</KeyValue>
                <KeyValue label="完整 batch digest">
                  {confirmation.digest}
                </KeyValue>
                <KeyValue label="Batch index 路徑">
                  {confirmation.path}
                </KeyValue>
                <KeyValue label="待審提交程式">
                  {confirmation.intake}
                </KeyValue>
                <KeyValue label="證據查詢程式">{confirmation.query}</KeyValue>
              </dl>
              <p>
                這是同一來源列的獨立單候選
                attempts。保留每筆收據與失敗階段，不會因聊天中出現 admit
                就執行。
              </p>
              <details open>
                <summary>
                  核對全部 {confirmation.count} 筆完整候選與限制
                </summary>
                {confirmation.candidates.map((candidate) => (
                  <CandidateCard
                    key={candidate.ordinal}
                    candidate={candidate}
                  />
                ))}
              </details>
            </>
          ) : (
            <>
              <div className="notice warning">
                請先確認伺服器角色與工具實作僅有你授權的來源操作。allowlist、名稱或描述都不證明工具唯讀；此確認不是
                AHE reviewer 核准。
              </div>
              <dl>
                <KeyValue label="伺服器">
                  {confirmation.connection.name}
                </KeyValue>
                <KeyValue label="連線 ID">
                  {confirmation.connection.id}
                </KeyValue>
                <KeyValue label="傳輸座標">
                  {confirmation.connection.transport === "stdio"
                    ? confirmation.connection.command
                    : confirmation.connection.url}
                </KeyValue>
                <KeyValue label="工具名稱">{confirmation.tool.name}</KeyValue>
                <KeyValue label="本次 allowlist">
                  {confirmation.connection.allowedTools.join(", ")}
                </KeyValue>
                <KeyValue label="工具綁定 digest">
                  {confirmation.tool.digest}
                </KeyValue>
                <KeyValue label="Schema SHA-256">
                  {confirmation.tool.schemaSHA256}
                </KeyValue>
                <KeyValue label="Inventory SHA-256">
                  {confirmation.tool.inventorySHA256}
                </KeyValue>
                <KeyValue label="Config SHA-256">
                  {confirmation.tool.configSHA256}
                </KeyValue>
              </dl>
              <h3>本次精確參數（不改寫）</h3>
              <Code label="本次精確工具參數">{confirmation.argsJSON}</Code>
            </>
          )}
        </Dialog>
      )}
    </div>
  );
}

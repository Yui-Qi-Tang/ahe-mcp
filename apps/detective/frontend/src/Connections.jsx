import React, { useState } from "react";
import { argumentsError } from "./bridge.js";
import { Badge, Code, Empty, Icon, KeyValue } from "./components.jsx";
import SourceToolAdvice from "./SourceToolAdvice.jsx";
import "./Connections.css";

function connectionIDErrors(connections) {
  return connections.map((connection) => {
    const id = connection.id;
    if (
      typeof id !== "string" ||
      id.length < 1 ||
      id.length > 128 ||
      /[^A-Za-z0-9_.-]/.test(id)
    ) {
      return "連線 ID 不可空白；僅限 ASCII 英數字與 _ . -，長度 1–128，不會自動移除空白或改寫。";
    }
    if (connections.filter((item) => item.id === connection.id).length > 1) {
      return "連線 ID 重複；請手動指定不同的精確 ID（區分大小寫）。";
    }
    return "";
  });
}

export default function Connections({
  state,
  draft,
  draftConflict,
  onDraftChange,
  onDiscardDraft,
  disabled,
  onAction,
  onToolConfirm,
}) {
  const fingerprint = JSON.stringify(state.settings);
  const [connectionID, setConnectionID] = useState("");
  const [toolName, setToolName] = useState("");
  const [argsJSON, setArgsJSON] = useState("{}");
  const changed = JSON.stringify(draft) !== fingerprint;
  const selectedConnection = state.settings.connections.find(
    (item) => item.id === connectionID,
  );
  const tools = state.tools.filter(
    (tool) => tool.connectionID === connectionID,
  );
  const selectedTool = tools.find((tool) => tool.name === toolName);
  const allowed = Boolean(selectedConnection?.allowedTools?.includes(toolName));
  const argsError = argumentsError(argsJSON);
  const local = state.settings.mode === "local";
  const idErrors = connectionIDErrors(draft.connections);
  const invalidIDs = idErrors.some(Boolean);
  const busyReason = disabled
    ? "桌面尚未就緒或正在處理操作，暫時不能變更設定或操作來源工具。"
    : "";
  const conflictReason = draftConflict
    ? "已生效設定在編輯期間變更；草稿仍保留，但不能覆蓋新設定。請先保留需要的內容，再放棄草稿並重新編輯。"
    : "";
  const applyReason =
    busyReason ||
    conflictReason ||
    (invalidIDs ? "請先修正下方連線 ID 欄位的錯誤，再套用設定。" : "") ||
    (!changed ? "沒有尚未套用的設定。" : "");
  const toolPrerequisite =
    busyReason ||
    conflictReason ||
    (invalidIDs ? "連線 ID 不符合限制；請修正並套用設定後再操作工具。" : "") ||
    (changed ? "有未套用的草稿；請先套用設定，再操作來源工具。" : "") ||
    (!local ? "目前仍是離線演練；請先選擇實際模式並套用設定。" : "");
  const discoveryReason =
    toolPrerequisite || (!selectedConnection ? "請先選擇已套用的伺服器。" : "");
  const selectionReason =
    discoveryReason ||
    (tools.length === 0 ? "請先按「取得工具清單」，再選擇工具。" : "");
  const parametersReason =
    toolPrerequisite ||
    (!selectedTool
      ? "請先取得工具清單，再明確選擇一個工具；參數不會自動產生。"
      : "");
  const callReason =
    parametersReason ||
    (!allowed ? "此工具不在已套用的 allowlist，不可呼叫。" : "") ||
    (!selectedTool?.digest
      ? "工具缺少綁定 digest，請重新取得工具清單。"
      : "") ||
    argsError;
  const update = (key, value) =>
    onDraftChange((current) => ({ ...current, [key]: value }));
  const updateConnection = (index, patch) =>
    onDraftChange((current) => ({
      ...current,
      connections: current.connections.map((connection, i) =>
        i === index ? { ...connection, ...patch } : connection,
      ),
    }));
  function addConnection() {
    if (disabled) return;
    let number = 1;
    while (
      draft.connections.some(
        (connection) => connection.id === `source-${number}`,
      )
    )
      number += 1;
    update("connections", [
      ...draft.connections,
      {
        id: `source-${number}`,
        name: "新的資料源",
        transport: "stdio",
        command: "",
        url: "",
        allowedTools: [],
      },
    ]);
  }
  function applySettings() {
    if (applyReason) return;
    onAction("SaveSettings", {
      ...draft,
      connections: draft.connections.map((connection) => ({
        ...connection,
        allowedTools: connection.allowedTools
          .map((name) => name.trim())
          .filter(Boolean),
      })),
    });
  }
  return (
    <div className="settings-page">
      <div className="page-intro">
        <p className="eyebrow">CONNECTIONS</p>
        <h1>資料源與連線</h1>
        <p>
          設定會保存於本機私有資料目錄，不保存金鑰或密碼。聊天不會自動保存；每次啟動仍從離線演練開始。
        </p>
      </div>
      <section className="surface settings-apply-bar" aria-label="設定套用狀態">
        <div className="settings-apply-summary" aria-live="polite">
          <Badge tone={local ? "warning" : ""}>
            {local ? "已生效：實際模式" : "已生效：離線演練"}
          </Badge>
          <p className={changed ? "draft-pending" : "muted"}>
            {changed
              ? `未套用草稿：${draft.mode === "local" ? "實際模式" : "離線演練"}；下方變更尚未生效。`
              : "設定與桌面狀態一致。"}
          </p>
          <p className="field-help">
            套用只保存設定，不會連線、探索工具或授予寫入權限。
          </p>
          <p className="field-help">
            未套用的設定草稿會在本次視窗切頁時保留；不會自動保存，關閉視窗後不保留。
            工具選擇、呼叫參數與模型建議不隨草稿保存。
          </p>
          {busyReason && (
            <p id="connections-busy-reason" className="field-help">
              {busyReason}
            </p>
          )}
        </div>
        <div className="settings-apply-action">
          <button
            className="button primary"
            disabled={Boolean(applyReason)}
            aria-describedby="settings-apply-reason"
            onClick={applySettings}
          >
            套用本次設定
          </button>
          {changed && (
            <button
              className="button secondary"
              disabled={disabled}
              onClick={onDiscardDraft}
            >
              放棄未套用草稿
            </button>
          )}
          <p
            id="settings-apply-reason"
            className={invalidIDs || draftConflict ? "error-text" : "field-help"}
          >
            {applyReason || "確認草稿後，按此套用。"}
          </p>
        </div>
      </section>
      <section className="surface settings-section">
        <div className="section-heading">
          <h2>工作模式與本機模型</h2>
        </div>
        <p className="field-help">
          模式選項是設定草稿；按頁首「套用本次設定」後才會生效。
        </p>
        <fieldset disabled={disabled}>
          <div className="mode-options">
            <label className={draft.mode === "demo" ? "selected" : ""}>
              <input
                type="radio"
                name="mode"
                value="demo"
                checked={draft.mode === "demo"}
                onChange={() => update("mode", "demo")}
              />
              <strong>離線演練</strong>
              <span>合成來源、確定性回覆。沒有真實模型或 DB 呼叫。</span>
            </label>
            <label className={draft.mode === "local" ? "selected" : ""}>
              <input
                type="radio"
                name="mode"
                value="local"
                checked={draft.mode === "local"}
                onChange={() => update("mode", "local")}
              />
              <strong>實際模式</strong>
              <span>
                明確使用本機模型、來源 MCP 與指定的 pending launcher。
              </span>
            </label>
          </div>
          <div className="form-grid">
            <label>
              模型名稱
              <input
                value={draft.model}
                onChange={(e) => update("model", e.target.value)}
                spellCheck={false}
              />
            </label>
            <label>
              本機模型 Base URL
              <input
                value={draft.baseURL}
                onChange={(e) => update("baseURL", e.target.value)}
                spellCheck={false}
                placeholder="http://127.0.0.1:11434/v1"
              />
            </label>
          </div>
          <p className="field-help">
            只接受本機 loopback。此版沒有 API key、雲端 fallback
            或自動選模功能。
          </p>
        </fieldset>
      </section>
      <section className="surface settings-section">
        <h2>AHE 查詢與 pending 交接</h2>
        <p className="muted">
          Query launcher 也供證據搜尋唯讀查詢，不需要模型、Intake 或 Review launcher。寫入仍須另外明確核准；聊天不能取得寫入權限。
        </p>
        <fieldset disabled={disabled}>
          <label>
            來源識別 ID
            <input
              value={draft.sourceID}
              onChange={(e) => update("sourceID", e.target.value)}
              spellCheck={false}
              placeholder="由操作者指定來源 ID"
            />
          </label>
          <div className="form-grid">
            <label>
              Intake launcher
              <input
                value={draft.intakeLauncher}
                onChange={(e) => update("intakeLauncher", e.target.value)}
                spellCheck={false}
                placeholder="/absolute/path/to/intake-launcher"
              />
            </label>
            <label>
              Query launcher
              <input
                value={draft.queryLauncher}
                onChange={(e) => update("queryLauncher", e.target.value)}
                spellCheck={false}
                placeholder="/absolute/path/to/query-launcher"
              />
            </label>
            <label>
              Review launcher
              <input
                value={draft.reviewLauncher || ""}
                onChange={(e) => update("reviewLauncher", e.target.value)}
                spellCheck={false}
                placeholder="/absolute/path/to/review-launcher"
              />
            </label>
          </div>
        </fieldset>
        <p className="field-help">
          填入操作者管理的單一可執行檔路徑，不是 shell 指令。憑證由
          launcher／服務端管理，不輸入 DSN、密碼或環境變數指派。
        </p>
        <p className="field-help">
          Brief 審查使用獨立 Review launcher；取得審查內容不代表核准。
          必須核對精確內容、選擇結果、填寫理由，再另外確認本次寫入。
        </p>
      </section>
      <section className="surface settings-section">
        <div className="section-heading">
          <h2>來源 MCP 伺服器</h2>
          <button
            className="button secondary compact"
            disabled={disabled}
            aria-describedby={disabled ? "connections-busy-reason" : undefined}
            onClick={addConnection}
          >
            <Icon name="add" size={16} />
            新增伺服器
          </button>
        </div>
        <p className="muted">
          HTTP 僅連本機 gateway，不會直接登入公司 SaaS；此版沒有
          OAuth。allowlist 與工具描述都不是唯讀權限的證明。
        </p>
        <p className="field-help">
          共同試用可選用已建置的 detective-source-demo：stdio
          填入該檔案的實際絕對路徑，allowlist 填 read_status，參數使用 {"{}"}
          。HTTP 範例由操作者先明確啟動本機服務；介面不會自動啟動它。
        </p>
        {draft.connections.length === 0 && (
          <Empty title="還沒有來源連線">
            新增 stdio launcher 或本機 Streamable HTTP
            gateway，再明確列出允許工具。
          </Empty>
        )}
        {draft.connections.map((connection, index) => (
          <fieldset
            className="connection-editor"
            disabled={disabled}
            key={index}
          >
            <div className="section-heading">
              <h3>伺服器 {index + 1}</h3>
              <button
                className="text-button danger-text"
                disabled={disabled}
                aria-describedby={
                  disabled ? "connections-busy-reason" : undefined
                }
                onClick={() => {
                  if (disabled) return;
                  update(
                    "connections",
                    draft.connections.filter((_, i) => i !== index),
                  );
                }}
              >
                移除此設定
              </button>
            </div>
            <div className="form-grid">
              <label>
                連線 ID
                <input
                  value={connection.id}
                  aria-label="連線 ID"
                  aria-invalid={Boolean(idErrors[index])}
                  aria-describedby={`connection-id-help-${index}`}
                  onChange={(e) =>
                    updateConnection(index, { id: e.target.value })
                  }
                  spellCheck={false}
                />
                <span
                  id={`connection-id-help-${index}`}
                  className={idErrors[index] ? "error-text" : "field-help"}
                >
                  {idErrors[index] ||
                    "ASCII 英數字與 _ . -，長度 1–128；ID 必須唯一且區分大小寫。"}
                  {idErrors[index] && (
                    <code className="connection-id-value">
                      目前值：{JSON.stringify(connection.id)}
                    </code>
                  )}
                </span>
              </label>
              <label>
                顯示名稱
                <input
                  value={connection.name}
                  onChange={(e) =>
                    updateConnection(index, { name: e.target.value })
                  }
                />
              </label>
            </div>
            <label>
              傳輸方式
              <select
                value={connection.transport}
                onChange={(e) =>
                  updateConnection(index, {
                    transport: e.target.value,
                    command: "",
                    url:
                      e.target.value === "streamable-http"
                        ? "http://127.0.0.1:3000/mcp"
                        : "",
                  })
                }
              >
                <option value="stdio">stdio · 單一 launcher</option>
                <option value="streamable-http">
                  Streamable HTTP · 本機 gateway
                </option>
              </select>
            </label>
            {connection.transport === "stdio" ? (
              <label>
                Launcher 絕對路徑
                <input
                  value={connection.command}
                  onChange={(e) =>
                    updateConnection(index, { command: e.target.value })
                  }
                  placeholder="/absolute/path/to/source-launcher"
                  spellCheck={false}
                />
              </label>
            ) : (
              <label>
                本機 gateway URL
                <input
                  value={connection.url}
                  onChange={(e) =>
                    updateConnection(index, { url: e.target.value })
                  }
                  placeholder="http://127.0.0.1:3000/mcp"
                  spellCheck={false}
                />
                <span className="field-help">
                  僅 http://127.0.0.1:port/path 或 http://[::1]:port/path；不放
                  token、密碼或自訂認證標頭。
                </span>
              </label>
            )}
            <label>
              工具 allowlist（每行一個精確名稱）
              <textarea
                value={(connection.allowedTools || []).join("\n")}
                onChange={(e) =>
                  updateConnection(index, {
                    allowedTools: e.target.value.split("\n"),
                  })
                }
                rows={3}
                spellCheck={false}
                placeholder="search_documents\nread_document"
              />
            </label>
          </fieldset>
        ))}
        <p className="field-help">
          修改完成後，請使用頁首固定顯示的「套用本次設定」。尚未套用的草稿不會建立連線。
        </p>
      </section>
      <section className="surface settings-section">
        <div className="section-heading">
          <h2>手動來源工具</h2>
          <Badge tone="warning">每次呼叫都需確認</Badge>
        </div>
        <ol className="connection-tool-steps" aria-label="來源工具操作步驟">
          <li>套用實際模式與連線設定</li>
          <li>選擇伺服器，取得工具清單</li>
          <li>手動選擇工具，填寫精確參數</li>
          <li>逐次檢查並批准呼叫</li>
        </ol>
        {!local && (
          <div className="notice">
            離線演練不會連線來源 MCP。請明確套用實際模式後再探索或呼叫工具。
          </div>
        )}
        <div className="tool-controls">
          <label>
            已套用的伺服器
            <select
              value={connectionID}
              disabled={Boolean(toolPrerequisite)}
              aria-describedby="tool-discovery-reason"
              onChange={(e) => {
                setConnectionID(e.target.value);
                setToolName("");
              }}
            >
              <option value="">選擇伺服器</option>
              {state.settings.connections.map((connection, index) => (
                <option key={index} value={connection.id}>
                  {connection.name} · {connection.id}
                </option>
              ))}
            </select>
          </label>
          <button
            className="button secondary"
            disabled={Boolean(discoveryReason)}
            aria-describedby="tool-discovery-reason"
            onClick={() => {
              if (!discoveryReason) {
                setToolName("");
                onAction("DiscoverTools", connectionID);
              }
            }}
          >
            取得工具清單
          </button>
        </div>
        <p id="tool-discovery-reason" className="field-help">
          {discoveryReason ||
            "按「取得工具清單」才會連線探索；不會執行任何工具。"}
        </p>
        {selectedConnection && (
          <dl className="tool-coordinate">
            <KeyValue label="精確 allowlist">
              {(selectedConnection.allowedTools || []).join(", ") ||
                "空白；所有工具均不可呼叫"}
            </KeyValue>
            <KeyValue label="傳輸座標">
              {selectedConnection.transport === "stdio"
                ? selectedConnection.command
                : selectedConnection.url}
            </KeyValue>
          </dl>
        )}
        <SourceToolAdvice state={state} connection={selectedConnection} tools={tools}
          prerequisite={discoveryReason} draftFingerprint={JSON.stringify(draft)}
          onAction={onAction} onUse={(name, argumentsJSON) => {
            setToolName(name);
            setArgsJSON(argumentsJSON);
          }} />
        <label>
          已探索工具
          <select
            value={toolName}
            disabled={Boolean(selectionReason)}
            aria-describedby="tool-selection-reason"
            onChange={(e) => setToolName(e.target.value)}
          >
            <option value="">選擇工具</option>
            {tools.map((tool) => (
              <option key={tool.name} value={tool.name}>
                {tool.name}
                {selectedConnection?.allowedTools?.includes(tool.name)
                  ? ""
                  : " · 不在 allowlist"}
              </option>
            ))}
          </select>
        </label>
        <p id="tool-selection-reason" className="field-help">
          {selectionReason ||
            `已取得 ${tools.length} 個工具；請自行選擇，不會自動選取或呼叫。`}
        </p>
        <div className="connection-parameters">
          <label>
            精確參數 JSON
            <textarea
              className="json-input"
              value={argsJSON}
              onChange={(e) => setArgsJSON(e.target.value)}
              disabled={Boolean(parametersReason)}
              aria-describedby="tool-parameters-reason"
              aria-invalid={Boolean(argsError)}
              rows={7}
              spellCheck={false}
            />
          </label>
          <p
            id="tool-parameters-reason"
            className={argsError ? "error-text" : "field-help"}
          >
            {parametersReason ||
              argsError ||
              "參數會逐字送出，不會自動修補；read_status 範例使用 {}。"}
          </p>
        </div>
        {selectedTool && (
          <div className="tool-preview">
            <h3>{selectedTool.name}</h3>
            <p className="preserve-text">
              {selectedTool.description || "伺服器未提供描述。"}
            </p>
            <dl>
              <KeyValue label="工具綁定 digest">{selectedTool.digest}</KeyValue>
              <KeyValue label="Schema SHA-256">
                {selectedTool.schemaSHA256}
              </KeyValue>
              <KeyValue label="Inventory SHA-256">
                {selectedTool.inventorySHA256}
              </KeyValue>
              <KeyValue label="Config SHA-256">
                {selectedTool.configSHA256}
              </KeyValue>
            </dl>
            <details>
              <summary>完整 input schema</summary>
              <Code>{selectedTool.inputSchemaJSON}</Code>
            </details>
          </div>
        )}
        <div className="connection-call-action">
          <p
            id="tool-call-reason"
            className={callReason ? "field-help" : "muted"}
          >
            {callReason ||
              "下一步只開啟確認視窗；確認完整工具與參數並同意後，才會呼叫。"}
          </p>
          <button
            className="button primary"
            disabled={Boolean(callReason)}
            aria-describedby="tool-call-reason"
            onClick={() => {
              if (!callReason)
                onToolConfirm({
                  connection: selectedConnection,
                  tool: selectedTool,
                  argsJSON,
                });
            }}
          >
            檢查並批准這次呼叫
            <Icon name="arrow" size={16} />
          </button>
        </div>
      </section>
    </div>
  );
}

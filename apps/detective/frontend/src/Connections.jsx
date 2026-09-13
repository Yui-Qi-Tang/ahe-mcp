import React, { useState } from "react";
import { argumentsError, desktopBridge } from "./bridge.js";
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
  const [connectionID, setConnectionID] = useState(() =>
    Object.entries(state.sourceAuth || {}).find(([, login]) => login.status === "waiting")?.[0] || "");
  const [toolName, setToolName] = useState("");
  const [argsJSON, setArgsJSON] = useState("{}");
  const [choosingCodebase, setChoosingCodebase] = useState(false);
  const [presetError, setPresetError] = useState("");
  async function addCodebase() {
    if (disabled || choosingCodebase || draftConflict) return;
    setChoosingCodebase(true); setPresetError("");
    try {
      const bridge = desktopBridge();
      if (!bridge?.ChooseCodebaseRepository) throw new Error("請由新版桌面程式開啟資料夾選擇器。");
      const preset = await bridge.ChooseCodebaseRepository();
      if (preset?.id) onDraftChange((current) => ({...current,
        connections: current.connections.some((c) => c.id === preset.id) ? current.connections : [...current.connections, preset]}));
    } catch (err) { setPresetError(String(err?.message || err)); }
    finally { setChoosingCodebase(false); }
  }
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
    (!local ? "目前尚未啟用連線；請先選擇實際模式並套用設定。" : "");
  const login = state.sourceAuth?.[connectionID];
  const atlassian = selectedConnection?.transport === "atlassian-oauth";
  const loginReason = toolPrerequisite || (!selectedConnection ? "請先選擇已套用的伺服器。" : "");
  const discoveryReason = loginReason || (atlassian && login?.status !== "connected" ? "請先完成 Atlassian OAuth 登入。" : "");
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
          設定會保存於本機私有資料目錄，不保存金鑰或密碼。每次啟動仍從離線開始；套用實際模式後才能使用模型與來源工具。
        </p>
      </div>
      <section className="surface settings-apply-bar" aria-label="設定套用狀態">
        <div className="settings-apply-summary" aria-live="polite">
          <Badge tone={local ? "warning" : ""}>
            {local ? "已生效：實際模式" : "已生效：離線"}
          </Badge>
          <p className={changed ? "draft-pending" : "muted"}>
            {changed
              ? `未套用草稿：${draft.mode === "local" ? "實際模式" : "離線"}；下方變更尚未生效。`
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
              <strong>離線</strong>
              <span>只檢視已載入內容；不連線、不呼叫模型或 DB。</span>
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
                可使用本機模型與來源工具；每次操作仍由你決定。
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
          Atlassian OAuth 可直連官方遠端 MCP；一般 HTTP 仍限本機 gateway。登入與工具呼叫是分開的操作。
        </p>
        <button className="button primary" aria-describedby={disabled ? "connections-busy-reason" : undefined} disabled={disabled || choosingCodebase || draftConflict} onClick={addCodebase}>
          加入本機 Codebase（選擇資料夾）
        </button>
        <p className="field-help">聊天室目前只開放此網路封鎖預設。選擇資料夾只新增草稿；套用後，建立索引與聊天讀取仍需分別操作。</p>
        {presetError && <p role="alert">{presetError}</p>}
        {draft.connections.length === 0 && (
          <Empty title="還沒有來源連線">
            選擇 Atlassian 帳號登入、本機程式或本機 HTTP 服務，再設定要使用的工具。
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
            {connection.codebaseCache ? <>
              <h3>{connection.name}</h3>
              <p>Repository：{connection.directory}</p>
              <Badge>網路封鎖 · 不自動索引／監看 · 不入庫</Badge>
              <details><summary>進階設定（固定預設）</summary>
                <Code label="Codebase 固定設定">{JSON.stringify(connection, null, 2)}</Code>
              </details>
              <button type="button" className="button secondary" disabled={Boolean(toolPrerequisite)}
                onClick={() => {if (!toolPrerequisite) onAction("IndexCodebase", connection.id);}}>建立／更新此資料夾索引（不入庫）</button>
              <p className="field-help">索引會讀取上方資料夾並寫入獨立本機快取，不修改 repository。完成後回工作台選擇此來源，取得工具清單再提問。</p>
            </> : <>
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
                    url: e.target.value === "atlassian-oauth" ? "https://mcp.atlassian.com/v2/mcp"
                      : e.target.value === "streamable-http" ? "http://127.0.0.1:3000/mcp" : "",
                    ...(e.target.value === "atlassian-oauth" ? { allowedTools: ["getAccessibleAtlassianResources", "getJiraIssue", "getConfluenceContent"] } : {}),
                  })
                }
              >
                <option value="atlassian-oauth">Atlassian 官方 MCP · OAuth</option>
                <option value="stdio">本機程式（stdio）</option>
                <option value="streamable-http">
                  Streamable HTTP · 本機 gateway
                </option>
              </select>
            </label>
            {connection.transport === "atlassian-oauth" ? (
              <p className="field-help">官方端點：https://mcp.atlassian.com/v2/mcp。套用後可登入；僅要求 Jira／Confluence 讀取與搜尋權限。</p>
            ) : connection.transport === "stdio" ? (
              <label>
                本機啟動程式
                <input
                  aria-label="本機啟動程式"
                  aria-describedby={`source-program-help-${index}`}
                  value={connection.command}
                  onChange={(e) =>
                    updateConnection(index, { command: e.target.value })
                  }
                  placeholder="貼上此資料源啟動檔的完整路徑"
                  spellCheck={false}
                />
                <span id={`source-program-help-${index}`} className="field-help">
                  使用資料源安裝說明提供的可執行啟動檔；不是資料文件、網址或整串終端機指令。
                  若尚未取得啟動檔，請先完成該資料源的 MCP 安裝；不要填入密碼。
                </span>
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
            </>}
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
            離線時不會連線來源 MCP。請明確套用實際模式後再探索或呼叫工具。
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
        {atlassian && <div className="notice" aria-label="Atlassian 登入">
          <p>{login?.status === "connected" ? "已登入 Atlassian（僅本次程式執行期間）" : login?.status === "waiting" ? "等待 Atlassian 授權完成" : "尚未登入 Atlassian"}</p>
          <button className="button secondary" disabled={Boolean(loginReason)}
            onClick={() => { if (!loginReason) { setToolName(""); onAction("BeginAtlassianLogin", connectionID); } }}>
            {login?.status === "connected" ? "重新登入 Atlassian" : "登入 Atlassian"}
          </button>
          {login?.status === "connected" && <button className="button secondary" disabled={Boolean(loginReason)}
            onClick={() => { if (!loginReason) { setToolName(""); onAction("DisconnectAtlassian", connectionID); } }}>清除本次登入</button>}
          {login?.status === "waiting" && login.authorizationURL && <label>
            Atlassian 授權連結（請複製並自行開啟）
            <textarea readOnly value={login.authorizationURL} rows={4} onFocus={(e) => e.target.select()} />
          </label>}
          <p className="field-help">登入連結約兩分鐘內有效，可用頁面上方的取消按鈕停止。Token 只留在記憶體；重新啟動、套用設定或切回離線後需重新登入。</p>
        </div>}
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
              "參數會逐字送出，不會自動修補；請依所選工具的 input schema 填寫。"}
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
      <section className="surface settings-section" aria-label="AHE 證據庫設定">
        <h2>AHE 證據庫（選用）</h2>
        <p className="muted">
          用來查詢資料庫中的既有證據，或處理待審資料。只聊天、取得來源內容，不需要填這一區。
        </p>
        <p className="field-help">
          查詢：{state.settings.queryLauncher ? "已填寫" : "未設定"} ·
          待審提交：{state.settings.intakeLauncher ? "已填寫" : "未設定"} ·
          人工審閱：{state.settings.reviewLauncher ? "已填寫" : "未設定"}。
          此處只顯示已套用的設定，不代表連線成功。
        </p>
        <details>
          <summary>進階連線設定</summary>
          <p className="field-help">
            這些欄位填的是由 AHE 安裝者設定並提供的可執行啟動檔完整路徑（launcher），不是資料庫網址、密碼或 shell 指令。
            目前安裝不會自動產生這些檔案；尚未準備好或不知道路徑時，可以先留空。
          </p>
          <fieldset disabled={disabled}>
            <label>
              證據查詢程式
              <input
                value={draft.queryLauncher}
                onChange={(e) => update("queryLauncher", e.target.value)}
                aria-describedby="ahe-query-help"
                spellCheck={false}
                placeholder="AHE 唯讀查詢啟動檔的完整路徑"
              />
            </label>
            <p id="ahe-query-help" className="field-help">
              供「證據搜尋」讀取既有資料，不寫入 DB。只需要搜尋時，填這一項即可，不需要本機模型。
            </p>
            <label>
              待審提交程式
              <input
                value={draft.intakeLauncher}
                onChange={(e) => update("intakeLauncher", e.target.value)}
                aria-describedby="ahe-intake-help"
                spellCheck={false}
                placeholder="AHE 待審提交啟動檔的完整路徑"
              />
            </label>
            <p id="ahe-intake-help" className="field-help">
              將候選送入待審區（pending），不是正式採納。僅供既有候選流程使用，不會把聊天內容自動入庫。
            </p>
            <label>
              人工審閱程式
              <input
                value={draft.reviewLauncher || ""}
                onChange={(e) => update("reviewLauncher", e.target.value)}
                aria-describedby="ahe-review-help"
                spellCheck={false}
                placeholder="AHE 人工審閱啟動檔的完整路徑"
              />
            </label>
            <p id="ahe-review-help" className="field-help">
              供既有 Brief 審閱流程取得精確內容；核對後明確選擇採納、拒絕或僅留紀錄，填寫理由並確認，才會寫入決定。
              單純填入設定不會執行審閱。
            </p>
            <label>
              來源識別 ID
              <input
                value={draft.sourceID}
                onChange={(e) => update("sourceID", e.target.value)}
                aria-describedby="ahe-source-id-help"
                spellCheck={false}
                placeholder="由待審資料提供者指定的識別碼"
              />
            </label>
            <p id="ahe-source-id-help" className="field-help">
              既有候選交接使用的來源識別碼；不是登入帳號，查詢既有證據不需修改。
            </p>
          </fieldset>
        </details>
      </section>
    </div>
  );
}

import React, { useEffect, useRef, useState } from "react";
import { argumentsError } from "./bridge.js";
import { Badge, Code, KeyValue } from "./components.jsx";

const toolFields = [
  "connectionID", "name", "description", "inputSchemaJSON", "digest",
  "schemaSHA256", "inventorySHA256", "configSHA256",
];

function sameTool(left, right) {
  return left && right && toolFields.every((field) => left[field] === right[field]);
}

function validTools(tools, connection) {
  return tools.length > 0 && tools.length <= 32 &&
    new Set(tools.map((tool) => tool.name)).size === tools.length &&
    tools.every((tool) => {
      try {
        const schema = JSON.parse(tool.inputSchemaJSON);
        return toolFields.every((field) => typeof tool[field] === "string") &&
          tool.connectionID === connection?.id && tool.name && tool.digest &&
          tool.schemaSHA256 && tool.inventorySHA256 && tool.configSHA256 &&
          tool.inventorySHA256 === tools[0].inventorySHA256 &&
          tool.configSHA256 === tools[0].configSHA256 &&
          connection?.allowedTools?.includes(tool.name) && schema?.type === "object";
      } catch {
        return false;
      }
    });
}

function matchesAdvice(advice, connection, question, tools, model) {
  if (!advice || typeof advice.id !== "string" || !advice.id ||
      advice.connectionID !== connection?.id || advice.question !== question ||
      advice.model !== model || typeof advice.createdAt !== "string" ||
      !Number.isFinite(Date.parse(advice.createdAt)) ||
      typeof advice.reason !== "string" || !advice.reason.trim()) return false;
  if (advice.outcome === "abstain")
    return !advice.tool && !advice.argumentsJSON;
  return advice.outcome === "suggested" &&
    tools.some((tool) => sameTool(tool, advice.tool)) &&
    typeof advice.argumentsJSON === "string" && !argumentsError(advice.argumentsJSON);
}

export default function SourceToolAdvice({
  state, connection, tools, prerequisite, draftFingerprint, onAction, onUse,
}) {
  const [question, setQuestion] = useState("");
  const [accepted, setAccepted] = useState(null);
  const [requesting, setRequesting] = useState(false);
  const [feedback, setFeedback] = useState("");
  const generation = useRef(0);
  const context = JSON.stringify([state.settings, draftFingerprint, connection?.id, tools, question]);
  const currentContext = useRef(context);
  currentContext.current = context;
  useEffect(() => {
    generation.current += 1;
    setAccepted(null);
    setFeedback("");
  }, [context]);
  useEffect(() => () => { generation.current += 1; }, []);
  useEffect(() => {
    if (accepted && (state.busy || state.error || prerequisite ||
        state.batchPath || state.batchDigest ||
        JSON.stringify(state.toolAdvice) !== JSON.stringify(accepted.advice)))
      setAccepted(null);
  }, [accepted, state.busy, state.error, prerequisite, state.batchPath, state.batchDigest, state.toolAdvice]);
  const bytes = new TextEncoder().encode(question).length;
  const batchReason = state.batchPath || state.batchDigest
    ? "目前有作用中的批次；請先完成或明確開始新工作，再請模型建議來源工具。" : "";
  const reason = prerequisite || batchReason ||
    (state.settings.mode !== "local" ? "請先套用實際模式；離線演練不會請求模型建議。" : "") ||
    (!validTools(tools, connection) ? "請先取得所選伺服器的有效工具清單（1–32 個），再請模型建議。" : "") ||
    (requesting ? "正在處理模型建議；尚未呼叫來源工具。" : "") ||
    (!question.trim() ? "請先輸入要請模型協助選擇工具的問題。" : "") ||
    (bytes > 4096 ? "問題超過 4096 UTF-8 bytes；請自行縮短，不會自動截斷。" : "");
  const visible = !reason && !state.busy && !state.error &&
    accepted?.context === context && JSON.stringify(accepted.advice) === JSON.stringify(state.toolAdvice) &&
    matchesAdvice(state.toolAdvice, connection, question, tools, state.settings.model)
    ? accepted.advice : null;

  async function suggest() {
    if (reason || state.busy) return;
    const ticket = ++generation.current;
    const previousID = state.toolAdvice?.id;
    setAccepted(null);
    setFeedback("");
    setRequesting(true);
    try {
      const result = await onAction("SuggestSourceTool", connection.id, question);
      if (ticket !== generation.current || currentContext.current !== context) return;
      const resultTools = result?.tools?.filter((tool) => tool.connectionID === connection.id);
      if (!result || result.busy || result.error || result.batchPath || result.batchDigest ||
          JSON.stringify(result.settings) !== JSON.stringify(state.settings) ||
          JSON.stringify(resultTools) !== JSON.stringify(tools) ||
          result.toolAdvice?.id === previousID ||
          !matchesAdvice(result.toolAdvice, connection, question, tools, state.settings.model)) {
        setFeedback("未取得可核對的新建議；未帶入草稿，也未呼叫來源工具。請檢查執行紀錄後再決定是否重試。");
        return;
      }
      setAccepted({ context, advice: result.toolAdvice });
    } catch {
      if (ticket === generation.current)
        setFeedback("模型建議未完成；未帶入草稿，也未呼叫來源工具。不會自動重試。");
    } finally {
      setRequesting(false);
    }
  }

  return (
    <section className="connection-parameters" aria-label="模型工具建議">
      <div className="section-heading">
        <h3>請模型協助選擇工具</h3>
        <Badge tone="warning">建議不是執行</Badge>
      </div>
      <p className="field-help">
        只傳送你輸入的問題與所選連線的工具名稱、描述及 schema 給已套用的本機模型；
        不傳送聊天、來源內容或 launcher 設定。這個按鈕不探索或呼叫來源 MCP，也不寫入 AHE。
      </p>
      <label>
        工具選擇問題
        <textarea value={question} rows={3} disabled={Boolean(prerequisite) || requesting}
          onChange={(event) => setQuestion(event.target.value)}
          aria-describedby="tool-advice-reason" aria-invalid={bytes > 4096} />
      </label>
      <p className="field-help">{bytes} / 4096 UTF-8 bytes；使用模型：{state.settings.model}</p>
      <button className="button secondary" disabled={Boolean(reason) || state.busy}
        aria-describedby="tool-advice-reason" onClick={suggest}>請模型建議工具</button>
      <p id="tool-advice-reason" className="field-help">
        {reason || "按下後只請本機模型產生一份建議；收到建議後仍需你明確選擇是否帶入草稿。"}
      </p>
      {feedback && <p role="status" className="field-help">{feedback}</p>}
      {visible && (
        <div className="tool-preview" aria-label="本次模型建議">
          <h4>{visible.outcome === "abstain" ? "模型沒有提出可用工具" : `建議工具：${visible.tool.name}`}</h4>
          <p className="preserve-text">{visible.reason}</p>
          <p className="notice">
            只通過格式與工具綁定檢查，不代表語意正確、工具唯讀或取得核准。
            請核對工具範圍、參數及風險；帶入草稿也不會執行工具。
          </p>
          <dl><KeyValue label="建議時間">{visible.createdAt}</KeyValue></dl>
          {visible.outcome === "suggested" && <>
            <Code label="建議參數 JSON">{visible.argumentsJSON}</Code>
            <p className="field-help">帶入會取代下方目前的工具與參數草稿；不會自動開啟呼叫確認視窗。</p>
            <button className="button secondary" onClick={() => {
              if (!visible) return;
              onUse(visible.tool.name, visible.argumentsJSON);
              setAccepted(null);
              setFeedback("已帶入工具與參數草稿，尚未執行。請檢查下方完整工具與參數，再決定是否開啟呼叫確認視窗。");
            }}>帶入工具與參數草稿</button>
          </>}
        </div>
      )}
    </section>
  );
}

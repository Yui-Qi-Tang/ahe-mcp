import React from "react";
import { Code } from "./components.jsx";

// This filter is presentation only. Both backend operations enforce the preset.
export function codebaseConnections(state) {
  return state.settings.connections.filter((c) => c.transport === "stdio" && c.codebaseCache && c.directory);
}

export default function CodebaseChat({ state, selected, onSelect, disabled, onAction }) {
  const connections = codebaseConnections(state);
  const request = state.sourceChat;
  const connection = connections.find((c) => c.id === request?.connectionID);
  const canConfirm = !disabled && state.settings.mode === "local" && connection &&
    request?.status === "awaiting_confirmation" && Date.parse(request.expiresAt) > Date.now();
  return <section aria-label="離線 Codebase 聊天" className="notice">
    <label>聊天讀取來源
      <select aria-label="聊天讀取來源" value={connections.some((c) => c.id === selected) ? selected : ""}
        disabled={disabled} onChange={(e) => onSelect(e.target.value)}>
        <option value="">只討論目前內容（不呼叫工具）</option>
        {connections.map((c) => <option key={c.id} value={c.id}>{c.name} · 網路封鎖</option>)}
      </select>
    </label>
    <p className="field-help">只開放離線 Codebase；每回合先建議、經你確認才讀取一次。可追問或取消，不會自動搜尋或入庫。</p>
    {connections.length === 0 && <p className="field-help">請到「資料源與連線」加入本機 Codebase。</p>}
    {connections.some((c) => c.id === selected) && <button type="button" className="button secondary" disabled={disabled || state.settings.mode !== "local"}
      onClick={() => onAction("DiscoverTools", selected)}>取得 Codebase 工具清單</button>}
    {request && <div aria-label="Codebase 讀取卡">
      <p>{connection?.name || "原連線已移除"} · {request.question}</p>
      <p>狀態：{({awaiting_confirmation:"等待你確認",needs_input:"需要補充問題",completed:"本回合完成",failed:"未完成；不自動重試",superseded:"已失效"})[request.status] || request.status}</p>
      <p>{request.advice?.reason}</p>
      {request.advice?.tool && <>
        <strong>{request.advice.tool.name}</strong>
        <Code label="本回合完整參數">{request.advice.argumentsJSON}</Code>
        <button type="button" className="button primary" disabled={!canConfirm}
          onClick={() => { if (canConfirm) onAction("ConfirmSourceChat", request.id); }}>確認本次讀取（不入庫）</button>
      </>}
      {request.source && <p>原始結果已保存，見右側來源與收據。程式關係是索引觀測；只有另行核對的程式原文才是引用，模型回答不是原文。</p>}
    </div>}
  </section>;
}

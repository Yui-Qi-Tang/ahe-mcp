import React from "react";
import { Badge, Code, KeyValue } from "./components.jsx";
import "./EvidenceSearch.css";

export const initialSearchDraft = () => ({ question: "", sourceID: "", admissionOutcome: "", lifecycleScope: "active", limit: "20" });
export const searchRequest = (draft) => ({ question: draft.question.trim(), sourceID: draft.sourceID.trim(), admissionOutcome: draft.admissionOutcome, lifecycleScope: draft.lifecycleScope, limit: Number(draft.limit) });
export const sameSearchRequest = (a, b) => Boolean(a && b && ["question", "sourceID", "admissionOutcome", "lifecycleScope", "limit"].every((key) => a[key] === b[key]));
export function searchDraftError(draft) {
  if (!draft.question.trim()) return "請先輸入查詢文字；不會自動搜尋。";
  if (Array.from(draft.question.trim()).length > 256) return "查詢文字限 256 個字元。";
  if (draft.question.trim().split(/\s+/u).length > 16) return "查詢文字限 16 個空白分隔詞。";
  if (!Number.isInteger(Number(draft.limit)) || Number(draft.limit) < 1 || Number(draft.limit) > 100) return "筆數上限須為 1–100 的整數。";
  return "";
}
const object = (value) => value && typeof value === "object" && !Array.isArray(value) ? value : {};
const text = (value) => typeof value === "string" ? value : Number.isSafeInteger(value) ? String(value) : "未提供";
const terms = (value) => Array.isArray(value) ? (value.map(text).join("、") || "（空清單）") : "未提供詞元清單";

export function parseSearchResult(search) {
  if (!search) return { response: null, error: "" };
  try {
    const response = JSON.parse(search.rawJSON);
    if (response?.schema_version !== "grounded-evidence-brief-v7" || !Array.isArray(response.matches) ||
        response.matches.some((item) => !item || typeof item !== "object" || Array.isArray(item)) ||
        !Number.isSafeInteger(search.returnedMatches) || search.returnedMatches !== response.matches.length) throw new Error("shape");
    return { response, error: "" };
  } catch {
    return { response: null, error: "回應格式不完整，不能判定為空結果。請查看原生 JSON 與執行紀錄。" };
  }
}

function RequestDetails({ request, title = "這次已執行的查詢" }) {
  const value = object(request);
  return <section className="search-request"><h3>{title}</h3><Code label={title + "文字"}>{text(value.question)}</Code>
    <dl><KeyValue label="來源 ID">{value.sourceID === "" ? "全部來源" : text(value.sourceID)}</KeyValue>
      <KeyValue label="處置篩選">{value.admissionOutcome === "" ? "全部處置" : text(value.admissionOutcome)}</KeyValue>
      <KeyValue label="生命週期">{text(value.lifecycleScope)}</KeyValue><KeyValue label="筆數上限">{text(value.limit)}</KeyValue></dl>
  </section>;
}

function Quote({ value, label }) {
  const ref = object(value);
  return <div className="search-quote"><Code label={label}>{text(ref.quoted_text)}</Code><details><summary>引用識別與 bytes</summary><dl>
    <KeyValue label="span ID">{text(ref.span_id)}</KeyValue><KeyValue label="view ID">{text(ref.extraction_view_id)}</KeyValue>
    <KeyValue label="起訖 bytes">{text(ref.start_byte) + "–" + text(ref.end_byte)}</KeyValue><KeyValue label="引用 hash">{text(ref.quoted_text_hash)}</KeyValue>
  </dl></details></div>;
}

function MatchCard({ match, index, sourceScopes }) {
  const record = object(match.record_ref), state = object(match.record_state), basis = object(match.retrieval_basis);
  const scopeRef = object(match.source_scope_ref);
  const scope = Array.isArray(sourceScopes) && typeof scopeRef.id === "string" && typeof scopeRef.kind === "string"
    ? sourceScopes.find((item) => object(object(item).scope_ref).id === scopeRef.id && object(object(item).scope_ref).kind === scopeRef.kind) : null;
  const source = object(object(scope).source), external = object(source.external_source);
  const refs = Array.isArray(match.source_refs) ? match.source_refs : null;
  const surfaces = Array.isArray(basis.surfaces) ? basis.surfaces : null;
  return <article className="surface search-match" aria-label={"搜尋記錄 " + (index + 1)}>
    <div className="search-heading"><h2>記錄 {index + 1}</h2><Badge>{text(match.admission_outcome)}</Badge></div>
    <Code label={"記錄 " + (index + 1) + " statement"}>{text(match.statement_text)}</Code>
    <dl><KeyValue label="記錄 ID">{text(record.id)}</KeyValue><KeyValue label="記錄類型">{text(record.kind)}</KeyValue>
      <KeyValue label="authority status">{text(state.authority_status)}</KeyValue><KeyValue label="record lifecycle">{text(state.record_lifecycle)}</KeyValue>
      <KeyValue label="來源 scope">{text(object(match.source_scope_ref).id)}</KeyValue>
      <KeyValue label="來源 ID">{text(source.source_id)}</KeyValue><KeyValue label="來源版本">{text(source.source_version)}</KeyValue>
      {typeof external.title === "string" && <KeyValue label="來源標題">{external.title}</KeyValue>}
      {typeof external.source_location === "string" && <KeyValue label="來源位置（純文字）">{external.source_location}</KeyValue>}
      <KeyValue label="canonical ID">{match.canonical_ref == null ? "未提供 canonical 參照" : text(object(match.canonical_ref).id)}</KeyValue></dl>
    <div className="search-columns"><section><h3>記錄的原引用</h3><p className="field-help">既有 source_refs，逐字呈現；不以搜尋片段取代。</p>
      {refs?.length ? refs.map((ref, i) => <Quote key={i} value={ref} label={"原引用 " + (index + 1) + "-" + (i + 1)} />) : <p>{refs ? "這筆記錄沒有列出原引用。" : "原引用欄位未提供或格式不明，請查看原生 JSON。"}</p>}
    </section><section><h3>本次搜尋命中依據</h3><p className="field-help">來源命中不是新的原引用，也不代表來源支持 statement 或語意正確。</p>
      <p>baseline_matched：{typeof basis.baseline_matched === "boolean" ? String(basis.baseline_matched) : "未提供"}</p>
      {surfaces?.length ? surfaces.map((item, i) => {
        const surface = object(item), spans = Array.isArray(surface.matched_spans) ? surface.matched_spans : [];
        return <section className="search-surface" key={i}><h4>{text(surface.surface)} · {text(surface.status)}</h4>
          <p>命中詞元：{terms(surface.matched_terms)}</p><p>未命中詞元：{terms(surface.missing_terms)}</p>
          {spans.map((item, j) => { const span = object(item); return <div key={j}><Quote value={span.source_ref} label={"搜尋片段 " + (index + 1) + "-" + (i + 1) + "-" + (j + 1)} />
            <p className="field-help">是否位於原引用範圍：{typeof span.within_proposal_source_refs === "boolean" ? String(span.within_proposal_source_refs) : "未提供"}；片段詞元：{terms(span.matched_terms)}</p></div>; })}
          {surface.spans_truncated === true && <p className="notice">此來源的搜尋片段已截斷，不是完整來源。</p>}
        </section>;
      }) : <p>未提供可顯示的搜尋 surface；請查看原生 JSON。</p>}
    </section></div>
  </article>;
}

export default function EvidenceSearch({ state, draft, onDraftChange, blockedReason, onSearch, onSettings, feedback }) {
  const pending = feedback?.status === "running" || feedback?.status === "cancel_requested";
  const unavailable = ["unconfirmed", "cancelled"].includes(feedback?.status);
  const search = pending || unavailable ? null : state.search;
  const { response, error } = parseSearchResult(search);
  const validation = searchDraftError(draft);
  const update = (key, value) => onDraftChange((current) => ({ ...current, [key]: value }));
  const execution = object(response?.query_execution);
  const policy = object(search?.hanTermPolicy);
  return <div className="page-scroll"><div className="evidence-search-page">
    <header className="page-intro"><p className="eyebrow">READ-ONLY EVIDENCE SEARCH</p><h1>證據搜尋</h1>
      <p>查詢 DB 既有記錄，不呼叫模型、不蒐集新來源、不寫入或取得人工核准。回傳筆數不是正確答案數。</p></header>
    <form className="surface search-form" onSubmit={(event) => { event.preventDefault(); if (!blockedReason && !validation) onSearch(searchRequest(draft)); }}>
      <label>查詢文字<textarea rows={3} value={draft.question} onChange={(event) => update("question", event.target.value)} placeholder="例如：地震 magnitude" /></label>
      <p className="field-help">這是尚未送出的草稿；切換頁面會保留，不會自動查詢或重試。</p>
      <details><summary>進階篩選</summary><div className="form-grid">
        <label>來源 ID 篩選<input value={draft.sourceID} onChange={(event) => update("sourceID", event.target.value)} placeholder="留空查全部來源" /></label>
        <label>處置篩選<select value={draft.admissionOutcome} onChange={(event) => update("admissionOutcome", event.target.value)}><option value="">全部處置</option><option value="admitted">admitted</option><option value="rejected">rejected</option><option value="audit_only">audit_only</option><option value="pending">pending</option></select></label>
        <label>生命週期篩選<select value={draft.lifecycleScope} onChange={(event) => update("lifecycleScope", event.target.value)}><option value="active">active</option><option value="historical">historical</option><option value="all">all</option></select></label>
        <label>筆數上限<input type="number" min="1" max="100" value={draft.limit} onChange={(event) => update("limit", event.target.value)} /></label>
      </div></details>
      <div className="search-actions"><button className="button primary" type="submit" disabled={Boolean(blockedReason || validation)}>查詢既有證據</button><button className="button secondary" type="button" onClick={onSettings}>前往連線設定</button></div>
      {(blockedReason || validation) && <p className="notice">{blockedReason || validation}</p>}
    </form>
    {pending && <section className="surface search-result" role="status"><h2>{feedback.status === "cancel_requested" ? "已要求取消，等待確認" : "搜尋處理中"}</h2><p>舊結果已收起；不會自動重試。取消不會改變 DB。</p>{feedback.request && <RequestDetails request={feedback.request} title="這次送出的查詢" />}</section>}
    {unavailable && <section className="surface search-result" role="alert"><h2>{feedback.status === "cancelled" ? "已要求取消／結果未採用" : "查詢未完成或結果未確認"}</h2><p>不顯示舊結果，也不能判定為查無資料。請檢查執行紀錄；不會自動重試。</p>{feedback.request && <RequestDetails request={feedback.request} title="這次送出的查詢" />}</section>}
    {!search && !feedback && <p className="notice">尚未執行查詢。確認文字與篩選後送出。</p>}
    {search && <section className="search-result" aria-label="搜尋結果"><div className="search-heading"><h2>搜尋結果</h2><Badge>{search.demo ? "離線合成示範 · 未查 DB" : "既有 DB 記錄 · 唯讀"}</Badge></div>
      <RequestDetails request={search.request} title={search.demo ? "固定示範條件（未查 DB）" : "這次已執行的查詢"} />
      {!sameSearchRequest(searchRequest(draft), search.request) && <p className="notice">上方草稿與以下結果條件不同；尚未依草稿重新搜尋。</p>}
      {error && <p role="alert" className="notice">{error}</p>}
      {response && <><p>回傳 {search.returnedMatches} 筆既有記錄；不代表相關率、正確答案數或新的審查決定。</p>
        <p>這些是既有處置；與問題無關的材料可略過，不需重新 reject。</p>
        <details><summary>搜尋方法與回應版本</summary><dl><KeyValue label="query mode">{text(execution.query_mode)}</KeyValue><KeyValue label="plan version">{text(execution.plan_version)}</KeyValue><KeyValue label="response schema">{text(response.schema_version)}</KeyValue></dl></details>
        {search.returnedMatches === 0 && <p className="notice">這次查詢沒有回傳記錄；不代表全域不存在相關證據。可自行調整文字或篩選後再查。</p>}
        {search.fallbackAttempted && <p className="notice">首輪沒有候選，已追加一次單英文詞元補查；這是較寬的字面檢索，不是推理或語意支持。</p>}
        {search.truncated && <p className="notice">結果已截斷；目前不是完整清單。</p>}
        {execution.search_complete_within_surface === false && <p className="notice">此次搜尋範圍未完整檢查，不能由空結果推論不存在。</p>}
        {Number.isSafeInteger(object(execution.multisurface).excluded_source_views) && execution.multisurface.excluded_source_views > 0 && <p className="notice">有 {execution.multisurface.excluded_source_views} 個來源檢視被排除；請查看原生回應的範圍與限制。</p>}
        {Object.keys(policy).length > 0 && <section className="notice"><p>中文輔助詞規則：{text(policy.version)}</p><p>本查詢遇到的輔助詞：{terms(policy.auxiliary_terms)}</p><p>可用中文固定字對：{terms(policy.eligible_anchor_terms)}</p><p>{policy.auxiliary_only_query_preserved ? "此查詢只有輔助詞，保留為短廣查；不代表主題或語意理解。" : "輔助詞不可單獨作為中文補充命中；原查詢與原引用不會因此刪改。"}</p></section>}
        {response.matches.map((match, index) => <MatchCard key={index} match={match} index={index} sourceScopes={response.source_scopes} />)}
        {Array.isArray(response.limitations) && response.limitations.length > 0 && <details><summary>原生回應的限制</summary>{response.limitations.map((item, index) => <p key={index}>{text(item)}</p>)}</details>}
      </>}
      <details className="search-raw"><summary>完整原生 JSON（原字串）</summary><Code label="搜尋原生 JSON">{typeof search.rawJSON === "string" ? search.rawJSON : "原生 JSON 字串未提供"}</Code></details>
    </section>}
  </div></div>;
}

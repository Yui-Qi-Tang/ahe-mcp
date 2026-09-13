import React, { useCallback, useEffect, useState } from "react";
import { Code, Dialog, KeyValue } from "./components.jsx";
import { stateLabel } from "./bridge.js";
import "./BriefWorkspace.css";

const coverageLabels = {
  full_document: "全文（輸入者宣告）",
  exact_excerpt: "精確摘錄",
  truncated_document: "截斷資料",
};

// This is a quotation preview, not a support judgment. The service checks the
// same source digest and line range again before saving a candidate.
export function briefQuote(body, start, end) {
  const lines = body.split("\n").map((line) => line.endsWith("\r") ? line.slice(0, -1) : line);
  if (lines.at(-1) === "") lines.pop();
  const first = Number(start);
  const last = Number(end);
  if (!Number.isInteger(first) || !Number.isInteger(last) || first < 1 ||
      last < first || last > lines.length || last - first >= 12) return "";
  const selected = lines.slice(first - 1, last);
  if (selected.some((line) => !line.trim() || line.includes("\r"))) return "";
  return selected.join("\n");
}

function confirmationIdentity(state) {
  return JSON.stringify({
    mode: state.settings.mode,
    intake: state.settings.intakeLauncher,
    query: state.settings.queryLauncher,
    reviewer: state.settings.reviewLauncher,
    brief: state.brief,
  });
}

export default function BriefWorkspace({ state, disabled, onAction, draft, onDraftChange }) {
  const work = state.brief;
  const report = work.report;
  const source = report.source;
  const [localDraft, setLocalDraft] = useState(null);
  const currentDraft = onDraftChange ? draft : localDraft;
  const changeDraft = onDraftChange || setLocalDraft;
  // Checkpoint paths change during ordinary progress. Drafts bind to the exact
  // source/model input and review instead; App clears them on an explicit open.
  const candidateIdentity = JSON.stringify({ input: report.input_sha256, body: report.body_sha256,
    source, text: report.text, stage: report.stage, model: work.model });
  const reviewIdentity = JSON.stringify({ candidateIdentity, submission: work.submission?.digest,
    review: work.review, mode: state.settings.mode, intake: state.settings.intakeLauncher,
    query: state.settings.queryLauncher, reviewer: state.settings.reviewLauncher });
  const candidateDraft = currentDraft?.candidate?.identity === candidateIdentity ? currentDraft.candidate
    : { identity: candidateIdentity, statement: report.text || "", startLine: "", endLine: "" };
  const reviewDraft = currentDraft?.review?.identity === reviewIdentity ? currentDraft.review
    : { identity: reviewIdentity, outcome: "", reason: "" };
  const { statement, startLine, endLine } = candidateDraft;
  const { outcome, reason } = reviewDraft;
  function editCandidate(key, value) {
    changeDraft({ candidate: { ...candidateDraft, [key]: value }, review: reviewDraft });
  }
  function editReview(key, value) {
    changeDraft({ candidate: candidateDraft, review: { ...reviewDraft, [key]: value } });
  }
  const [confirmation, setConfirmation] = useState(null);
  const [error, setError] = useState("");
  const local = state.settings.mode === "local";
  const eligible = source.version === "detective-brief-source/v2" && ["news", "public_event"].includes(source.source_kind);
  const inactive = disabled || !local;
  const quote = work.submission ? work.submission.citation.exact_quote : briefQuote(source.body, startLine, endLine);
  const review = work.review;
  const displayID = review?.display?.review_display_artifact_id;
  const identity = confirmationIdentity(state);
  const hasHandoff = Boolean(work.handoff?.proposal_occurrence_id);
  const terminal = ["admitted", "rejected", "audit_only", "admit_verified", "reject_verified", "admitted_verified", "rejected_verified", "audit_only_verified"].includes(work.outcome);
  const canPrepare = !inactive && eligible && report.stage === "complete" && !work.submission &&
    !hasHandoff && statement.trim() && quote.trim();
  const canSubmit = !inactive && eligible && Boolean(work.submission?.digest) && !hasHandoff &&
    Boolean(state.settings.intakeLauncher && state.settings.queryLauncher);
  const canReview = !inactive && hasHandoff && !terminal && !work.decision &&
    Boolean(state.settings.reviewLauncher && state.settings.queryLauncher);
  const canApply = canReview && Boolean(displayID && review?.display?.payload_utf8) &&
    ["admit", "audit_only", "reject"].includes(outcome) && Boolean(reason.trim());
  // Query observes lifecycle, not the writer's decision ID or reason. Only a
  // matching raw terminal without either acknowledgement can recover a receipt.
  const recoverDecisionReceipt = Boolean(work.decision) && !work.admission && !work.disposition &&
    ["admitted", "rejected", "audit_only"].includes(work.outcome) &&
    ({ admit: "admitted", reject: "rejected", audit_only: "audit_only" })[work.decision.outcome] === work.outcome;
  // A successful pending read clears the query failure, not the saved decision.
  const showDecisionRetry = Boolean(work.decision) && (recoverDecisionReceipt ||
    (!terminal && (Boolean(work.failureStage) || work.outcome === "pending")));
  const canRetryDecision = !inactive && showDecisionRetry && hasHandoff &&
    Boolean(work.submission?.digest && displayID && review?.display?.payload_utf8 &&
      state.settings.reviewLauncher && state.settings.queryLauncher) &&
    work.decision.displayID === displayID &&
    work.decision.reviewLauncher === state.settings.reviewLauncher &&
    work.decision.queryLauncher === state.settings.queryLauncher;

  useEffect(() => {
    setConfirmation(null);
    setError("");
  }, [candidateIdentity]);
  useEffect(() => {
    setConfirmation(null);
  }, [displayID]);
  const close = useCallback(() => setConfirmation(null), []);

  function request(kind) {
    if (kind === "pending" && !canSubmit) return;
    if (kind === "review" && !canApply) return;
    if (kind === "review_retry" && !canRetryDecision) return;
    setError("");
    setConfirmation({
      kind, identity, digest: work.submission?.digest,
      displayID,
      outcome: kind === "review_retry" ? work.decision.outcome : outcome,
      reason: kind === "review_retry" ? work.decision.reason : reason,
      submission: work.submission,
      review,
      intake: state.settings.intakeLauncher,
      query: state.settings.queryLauncher,
      reviewer: state.settings.reviewLauncher,
    });
  }

  async function confirm() {
    const intent = confirmation;
    setConfirmation(null);
    if (!intent || inactive || intent.identity !== identity) {
      setError("來源、候選、審查內容或 launcher 已變動；本次確認已失效，請重新核對。");
      return;
    }
    if (intent.kind === "pending" && canSubmit) {
      await onAction("SubmitBriefPending", intent.digest);
    } else if (intent.kind === "review" && canApply &&
        intent.outcome === outcome && intent.reason === reason) {
      await onAction("ApplyBriefReview", {
        displayID: intent.displayID, outcome: intent.outcome, reason: intent.reason,
      });
    } else if (intent.kind === "review_retry" && canRetryDecision &&
        intent.outcome === work.decision.outcome && intent.reason === work.decision.reason) {
      await onAction("ApplyBriefReview", {
        displayID: intent.displayID, outcome: intent.outcome, reason: intent.reason,
      });
    } else {
      setError("決定已變更或目前不能執行；請重新檢查，不會自動重試。");
    }
  }

  return (
    <div className="brief-workspace page-scroll">
      <header className="page-intro">
        <p className="eyebrow">BRIEF → PENDING → REVIEW</p>
        <h1>閱讀來源，再逐步交接</h1>
        <p>來源、模型摘要、候選與 DB 狀態分開保留。引文存在不代表支持主張，聊天與歷史 admit 都不能代替本次決定。</p>
      </header>
      {!local && <p className="notice" role="note">目前為離線檢視；可查看已保存工作，不會呼叫模型、MCP 或資料庫。需明確切換實際模式才能操作。</p>}
      <p className="notice" role="note">Brief 僅供明確選定的新聞／公開事件閱讀，不用於工程文件、程式碼或 git 擷取。摘要可能漏項；精確引用與沒有捏造不代表擷取完整。</p>
      {!eligible && <p className="notice" role="note">這是未分類的舊紀錄或不適用 Brief 的來源，不能抽取、建立新候選或重送 pending。既有收據仍可查詢及精確審閱；不補填類型或改寫歷史資料。</p>}
      {error && <p className="error-text" role="alert">{error}</p>}
      <div className="brief-comparison">
        <section className="surface brief-section" aria-label="Brief 本次提供的來源">
          <h2>本次提供的原文</h2>
          <p className="field-help">以下來源座標由輸入者宣告；沒有因此證明發布者身分、內容真實性或目前狀態。</p>
          <p className="field-help">模型只會閱讀本次提供的文字；未提供的章節、欄位或連結內容不在本次範圍內。全文標示也是輸入者宣告，不是已核對發布者全文。</p>
          <dl>
            <KeyValue label="來源 ID">{source.source_id}</KeyValue>
            <KeyValue label="宣告來源類型">{source.source_kind || "舊版未分類"}</KeyValue>
            <KeyValue label="來源 URL">{source.source_url}</KeyValue>
            <KeyValue label="來源 revision">{source.source_revision}</KeyValue>
            <KeyValue label="觀測時間">{source.observed_at}</KeyValue>
            <KeyValue label="來源涵蓋範圍">{coverageLabels[source.coverage] || source.coverage || "未宣告"}</KeyValue>
            <KeyValue label="來源 SHA-256">{report.body_sha256}</KeyValue>
          </dl>
          {source.excerpt ? <section aria-label="Brief 摘錄追溯">
            <h3>已保存的父來源與摘錄位置</h3>
            <p className="field-help">這些是隨工作保存的追溯座標；開啟此畫面不會載入或重新核對父來源。可用 brief verify-excerpt 搭配原始父來源檔離線核對，核對成功也不代表來源內容為真或摘錄已涵蓋全文重點。</p>
            <dl>
              <KeyValue label="父來源 ID">{source.excerpt.parent_source_id}</KeyValue>
              <KeyValue label="父來源 revision">{source.excerpt.parent_source_revision}</KeyValue>
              <KeyValue label="父本文 SHA-256">{source.excerpt.parent_body_sha256}</KeyValue>
              <KeyValue label="父本文 bytes">{source.excerpt.parent_body_bytes}</KeyValue>
              <KeyValue label="摘錄 byte 範圍（起點含、終點不含）">{`${source.excerpt.start_byte}–${source.excerpt.end_byte}`}</KeyValue>
              <KeyValue label="選取理由">{source.excerpt.selection_reason}</KeyValue>
            </dl>
          </section> : source.coverage === "exact_excerpt" && <p className="notice" role="note">這份摘錄沒有父本文雜湊與位置紀錄；可檢視本次文字，但不能據此核對父來源或推論全文涵蓋程度。</p>}
          <Code label="來源限制">{JSON.stringify(source.limitations, null, 2)}</Code>
          <Code label="Brief 本次提供的原文">{source.body}</Code>
          <details>
            <summary>選引文用行號（不取代上方本次原文）</summary>
            <ol className="brief-source-lines">
              {source.body.split("\n").map((text, index) => <li key={index}><code>{text || " "}</code></li>)}
            </ol>
          </details>
        </section>
        <section className="surface brief-section" aria-label="Brief 模型與候選">
          <h2>1. 模型閱讀摘要</h2>
          <p className="field-help">WorldMonitor-style 單次閱讀；模型沒有工具與審查權限。摘要不會自行成為證據。</p>
          <dl>
            <KeyValue label="本次模型">{work.model}</KeyValue>
            <KeyValue label="Prompt 版本">{report.prompt_version}</KeyValue>
            <KeyValue label="閱讀階段">{stateLabel(report.stage)}</KeyValue>
          </dl>
          <Code label="模型 Brief 原文">{report.text || report.raw_text}</Code>
          <button className="button primary" disabled={inactive || !eligible || report.stage !== "inspected" || work.outcome !== "inspected" || Boolean(work.submission) || hasHandoff}
            onClick={() => onAction("ExtractBrief")}>以本機模型產生 Brief</button>
          <p className="field-help">每份工作只做一次模型閱讀；完成或失敗的結果保留，不在這份工作中重新抽取。若要開始新試驗，請使用新工作。</p>
          <h2>2. 明確選定主張與精確引文</h2>
          <p className="field-help">主張可從模型摘要修訂；引文必須選完整非空白來源行，最多 12 行。CRLF 換行依 manual-line-v1 投影為 LF，本次提供的原文仍保留。這一步只保存本機候選，不寫 DB，也不宣稱語意已核對。</p>
          <p className="field-help">未保存的主張、行號與審查理由只在本視窗切頁時保留；開啟另一份工作或關閉視窗後不保留。切頁會撤銷已開啟的確認，不會自動保存或送出。</p>
          <fieldset disabled={disabled || Boolean(work.submission) || hasHandoff}>
            <label>候選主張<textarea aria-label="Brief 候選主張" value={work.submission ? work.submission.statement : statement} rows={5}
              onChange={(event) => editCandidate("statement", event.target.value)} /></label>
            <div className="form-grid">
              <label>引文起始行<input aria-label="Brief 引文起始行" type="number" min="1" value={work.submission ? work.submission.citation.start_line : startLine}
                onChange={(event) => editCandidate("startLine", event.target.value)} /></label>
              <label>引文結束行<input aria-label="Brief 引文結束行" type="number" min="1" value={work.submission ? work.submission.citation.end_line : endLine}
                onChange={(event) => editCandidate("endLine", event.target.value)} /></label>
            </div>
          </fieldset>
          <Code label="Brief 選定精確引文">{quote}</Code>
          <button className="button secondary" disabled={!canPrepare}
            onClick={() => onAction("PrepareBriefCandidate", {
              statement, startLine: Number(startLine), endLine: Number(endLine), sourceSHA256: report.body_sha256,
            })}>保存這筆本機候選</button>
          {work.submission && <>
            <h3>已保存的完整候選與來源綁定</h3>
            <Code label="Brief 完整候選綁定">{JSON.stringify(work.submission, null, 2)}</Code>
          </>}
        </section>
      </div>
      <section className="surface brief-section" aria-label="Brief pending 與審查">
        <h2>3. Pending 交接與正式審查</h2>
        <p>Pending 會寫入 DB，但不是 canonical 採納。只有下方針對本次精確審查內容的獨立決定，才可要求 admit、audit_only 或 reject。</p>
        <dl>
          <KeyValue label="已保存工作路徑">{work.path}</KeyValue>
          <KeyValue label="工作完整性 digest">{work.digest}</KeyValue>
          <KeyValue label="本次 DB 查回／操作狀態">{stateLabel(work.outcome)}</KeyValue>
          <KeyValue label="失敗階段">{work.failureStage || "無已記錄的失敗"}</KeyValue>
          <KeyValue label="Proposal occurrence ID">{work.handoff?.proposal_occurrence_id}</KeyValue>
        </dl>
        <p className="field-help">以上是已保存或最近查回的狀態，不是 DB 即時訂閱。失敗、取消或逾時不代表寫入已回復。</p>
        <div className="brief-actions">
          <button className="button primary" disabled={!canSubmit} onClick={() => request("pending")}>核對並送出這筆 Brief pending</button>
          <button className="button secondary" disabled={inactive || !hasHandoff || !state.settings.queryLauncher}
            onClick={() => onAction("QueryBriefPending")}>查回這筆 Brief 狀態</button>
          <button className="button secondary" disabled={!canReview}
            onClick={() => onAction("PrepareBriefReview", work.submission.digest)}>取得這筆精確審查內容</button>
        </div>
        {!state.settings.reviewLauncher && <p className="field-help">尚未設定 Review launcher；不能取得或送出正式審查。</p>}
        {review && <>
          <h3>完整 MCP 審查內容（不摘要、不改寫）</h3>
          <Code label="Brief 完整 MCP 審查內容">{review.display.payload_utf8}</Code>
          <Code label="Brief 精確審查 subject">{JSON.stringify(review.subject, null, 2)}</Code>
          <dl><KeyValue label="審查內容識別碼">{displayID}</KeyValue></dl>
          <fieldset disabled={!canReview}>
            <legend>本次審查結果（沒有預設選項）</legend>
            <div className="brief-outcomes">
              {[["admit", "admit · 採納"], ["audit_only", "audit_only · 僅供查核"], ["reject", "reject · 拒絕"]].map(([value, label]) =>
                <label key={value}><input type="radio" name="brief-outcome" value={value} checked={outcome === value}
                  onChange={() => editReview("outcome", value)} />{label}</label>)}
            </div>
            <label>本次具體理由<textarea aria-label="Brief 本次具體理由" value={reason} rows={4}
              onChange={(event) => editReview("reason", event.target.value)} /></label>
          </fieldset>
          <p className="field-help">請說明來源如何支持或不支持這個主張。表單不驗證真人身分；模擬操作仍是模擬，不宣稱真人審查完成。</p>
          <button className="button primary" disabled={!canApply} onClick={() => request("review")}>檢查本次審查決定</button>
        </>}
        {work.decision && <>
          <h3>已保存決定（不等於寫入已確認）</h3>
          <Code label="Brief 已保存決定">{JSON.stringify(work.decision, null, 2)}</Code>
          {showDecisionRetry && <>
            <p className="notice warning">{recoverDecisionReceipt
              ? "已保存或最近查回的終態與原決定一致，但本機缺少寫入收據。查詢不能證明是這份決定或理由造成此結果；只能核對原決定與原 launcher，明確確認後再次呼叫正式審查入口，由 MCP 校驗是否可重播。不會自動重試。"
              : work.outcome === "pending"
              ? "最近一次精確查回為 pending，不代表先前操作已回復。若要接續，仍須核對同一份已保存決定與原 launcher，再明確確認；不會自動重試。"
              : "上次處置尚未完整確認，可能已生效。可先查回狀態；若要重試，只能使用下面同一份已保存決定與原 launcher，不重新產生候選或修改理由。"}</p>
            <button className="button secondary" disabled={!canRetryDecision} onClick={() => request("review_retry")}>
              {recoverDecisionReceipt ? "核對原決定以取回收據" : "核對並重試已保存決定"}
            </button>
          </>}
        </>}
        <details><summary>完整 Brief 工作與收據</summary><Code>{JSON.stringify(work, null, 2)}</Code></details>
      </section>
      {confirmation && <Dialog
        key={identity}
        title={confirmation.kind === "pending" ? "確認這筆 Brief pending 寫入" : confirmation.kind === "review_retry" ? "核對同一份已保存審查決定" : "確認這筆 Brief 審查決定"}
        onClose={close} onConfirm={confirm} disabled={inactive || confirmation.identity !== identity}
        confirmLabel={confirmation.kind === "pending" ? "同意本次 Brief pending 寫入" : "送出本次精確審查決定"}>
        <p className="notice warning">只對這份完整內容執行一次；取消不保證已發生的寫入會回復。這個確認不驗證真人身分，也不證明候選或理由正確。</p>
        <dl>
          <KeyValue label="Intake launcher">{confirmation.intake}</KeyValue>
          <KeyValue label="Query launcher">{confirmation.query}</KeyValue>
          {confirmation.kind !== "pending" && <KeyValue label="Review launcher">{confirmation.reviewer}</KeyValue>}
        </dl>
        {confirmation.kind === "pending" ? <Code label="待確認完整 Brief 候選">{JSON.stringify(confirmation.submission, null, 2)}</Code> : <>
          <Code label="待確認完整 MCP 審查內容">{confirmation.review.display.payload_utf8}</Code>
          <Code label="待確認精確審查 subject">{JSON.stringify(confirmation.review.subject, null, 2)}</Code>
          <dl><KeyValue label="審查內容識別碼">{confirmation.displayID}</KeyValue><KeyValue label="本次結果">{confirmation.outcome}</KeyValue></dl>
          <Code label="待確認原始理由">{confirmation.reason}</Code>
        </>}
      </Dialog>}
    </div>
  );
}

import React, { useCallback, useEffect, useRef, useState } from "react";
import { desktopBridge } from "./bridge.js";
import { Badge, Code, Dialog, KeyValue } from "./components.jsx";

// Match the CLI's visible control-character rendering; stored source bytes and
// the backend's byte coordinates are never changed by this display projection.
export function taskDisplayText(value) {
  return String(value ?? "").replace(/\r\n/g, "\n").replace(/[\p{Cc}\p{Cf}]/gu,
    (character) => character === "\n" || character === "\t" ? character
      : `\\u${character.codePointAt(0).toString(16).padStart(4, "0")}`);
}

function TaskText({ label, children }) {
  return <Code label={label}>{taskDisplayText(children)}</Code>;
}

function List({ label, values }) {
  return <KeyValue label={label}>{values?.length ? values.map(taskDisplayText).join("、") : "（無）"}</KeyValue>;
}

export default function TaskWorkspace({ state, disabled, settingsChanged, draft, onDraftChange, onAction, onSettings }) {
  const work = state.task;
  const current = draft || { objective: "", preparedInputID: "", binding: "" };
  const source = state.source;
  const binding = JSON.stringify({ path: source?.path, sha256: source?.sha256, id: source?.id,
    mode: state.settings.mode, model: state.settings.model, baseURL: state.settings.baseURL, settingsChanged });
  const latest = useRef({ current, binding });
  latest.current = { current, binding };
  const [confirmation, setConfirmation] = useState(null);
  const [feedback, setFeedback] = useState("");
  const prepared = work?.status === "prepared" && !work.record && work.inputID === current.preparedInputID &&
    current.binding === binding && current.objective === work.input?.task?.objective;
  const sourceReason = source?.kind === "task_record" ? "目前是保存紀錄，只供檢視。要執行新任務，請到資料源與連線取得來源。"
    : !source?.path || !source?.sha256 ? "請先到資料源與連線，確認工具與參數，取得一份來源。" : "";
  const modelReason = state.settings.mode !== "local" ? "選段需切換為實際模式並套用設定；離線模式不會假裝呼叫模型。"
    : !state.settings.model || !state.settings.baseURL ? "請先在連線設定明選本機模型與 endpoint。" : "";
  const blocked = disabled ? "桌面尚未連線或正在處理操作；請等待完成，必要時使用上方取消操作。"
    : settingsChanged ? "有尚未套用的設定草稿；請先套用或捨棄。" : sourceReason || modelReason;
  const objectiveError = !current.objective.trim() ? "請先說明這次想找什麼。"
    : new TextEncoder().encode(current.objective).length > 2048 ? "目的超過 2,048 bytes；請縮短目的後重新確認，不會自動截短。" : "";
  const prepareDisabled = Boolean(blocked || objectiveError || typeof desktopBridge()?.PrepareTask !== "function");
  const runDisabled = Boolean(blocked || !prepared || typeof desktopBridge()?.RunTask !== "function");
  const record = work?.record;
  const result = record?.status === "selected" || record?.status === "abstained" ? record.result : null;
  const scope = result?.scope || work?.scope;
  const frozenSource = work?.input?.source;
  const units = work?.units || [];
  const unselected = new Set(scope?.unselected_unit_ids || []);
  const close = useCallback(() => setConfirmation(null), []);

  useEffect(() => {
    setConfirmation(null);
  }, [binding, current.objective, current.preparedInputID, work?.inputID, work?.status]);

  function editObjective(objective) {
    onDraftChange({ objective, preparedInputID: "", binding: "" });
    setFeedback("");
  }

  async function prepare() {
    if (prepareDisabled) return;
    const expected = current;
    const expectedBinding = binding;
    const accepted = await onAction("PrepareTask", { objective: current.objective, sourcePath: source.path, sourceSHA256: source.sha256 });
    if (latest.current.current !== expected || latest.current.binding !== expectedBinding) return;
    if (accepted?.task?.status === "prepared" && !accepted.error && accepted.task.input?.task?.objective === expected.objective) {
      onDraftChange({ ...expected, preparedInputID: accepted.task.inputID, binding: expectedBinding });
      setFeedback("目的與來源已凍結。請核對下方原文、範圍與模型，再執行一次選段。");
    } else setFeedback("準備未確認完成；沒有呼叫選段模型。請查看錯誤與執行紀錄。");
  }

  async function run() {
    const intent = confirmation;
    setConfirmation(null);
    if (!intent || runDisabled || intent.inputID !== work.inputID || intent.binding !== binding || intent.objective !== current.objective) return;
    setFeedback("");
    await onAction("RunTask", intent.inputID);
  }

  return <div className="page-scroll"><main className="task-workspace">
    <header className="page-intro">
      <p className="eyebrow">TASK-DRIVEN SELECTION</p>
      <h1>你想從這份來源找什麼？</h1>
      <p>先確認目的與來源，再讓本機模型選段。候選由程式回填原文，不用摘要取代正文。</p>
    </header>
    <p className="notice">這一版只產生本機候選，尚未接入 pending 或 admit／reject／audit_only。不是人工核准，也不會寫入 DB。</p>
    <section className="surface task-input" aria-label="任務目的與來源">
      <div className="task-actions">
        <button className="button secondary" disabled={disabled} onClick={onSettings}>資料源與模型設定</button>
      </div>
      {source ? <dl>
        <KeyValue label="目前來源">{taskDisplayText(source.title)}</KeyValue>
        <KeyValue label="已保存來源路徑">{taskDisplayText(source.path)}</KeyValue>
        <KeyValue label="來源 SHA-256">{taskDisplayText(source.sha256)}</KeyValue>
      </dl> : <p>{sourceReason}</p>}
      <label htmlFor="task-objective">這次採集目的</label>
      <textarea id="task-objective" rows={3} value={current.objective} disabled={disabled}
        placeholder="例如：找出這張 ticket 的回復方案、適用例外與失敗紀錄。"
        onChange={(event) => editObjective(event.target.value)} />
      <p className="field-help">這是任務輸入，不是自由對話；不會自動選工具、取得其他資料或補出未提供的內容。改目的後須重新確認。</p>
      <div className="task-actions">
        <button className="button primary" disabled={prepareDisabled} onClick={prepare}>確認目的與來源</button>
        <button className="button primary" disabled={runDisabled}
          onClick={() => setConfirmation({ inputID: work.inputID, binding, objective: current.objective })}>執行一次原文選段</button>
      </div>
      {(blocked || objectiveError) && <p className="field-help" role="status">{blocked || objectiveError}</p>}
      {feedback && <p role="status">{feedback}</p>}
      {work && !prepared && work.status === "prepared" && <p className="notice">此凍結工作未綁定目前目的、來源或模型設定。請重新確認，不可直接執行。</p>}
    </section>
    {work && <>
      <section className="surface task-frozen" aria-label="凍結任務">
        <div className="section-heading"><h2>本次凍結輸入</h2><Badge>{({ prepared: "已準備，尚未呼叫模型", running: "選段執行中", selected: "原文候選已產生", abstained: "模型棄答，沒有候選", failed: "選段未完成" })[work.status] || work.status}</Badge></div>
        <p className="field-help">以下始終顯示保存當時的目的與來源，不會套用上方新草稿。來源缺少欄位與模型未選段落分開呈現。</p>
        <TaskText label="凍結採集目的">{work.input?.task?.objective}</TaskText>
        <dl>
          <KeyValue label="任務版本">{work.input?.task?.revision}</KeyValue>
          <KeyValue label="凍結模型">{taskDisplayText(work.model)}</KeyValue>
          <KeyValue label="本次輸入 ID">{taskDisplayText(work.inputID)}</KeyValue>
          <KeyValue label="保存紀錄路徑">{taskDisplayText(work.path) || "尚未保存"}</KeyValue>
          <KeyValue label="來源標題">{taskDisplayText(frozenSource?.title)}</KeyValue>
          <KeyValue label="來源 ID">{taskDisplayText(frozenSource?.id)}</KeyValue>
          <KeyValue label="來源位置">{taskDisplayText(frozenSource?.location)}</KeyValue>
          <KeyValue label="來源 revision">{taskDisplayText(frozenSource?.revision)}</KeyValue>
          <KeyValue label="來源 coverage（宣告）">{taskDisplayText(frozenSource?.coverage)}</KeyValue>
          <List label="來源限制" values={frozenSource?.limitations} />
          <List label="任務要求欄位" values={scope?.requested_parts} />
          <List label="本次提供給模型" values={scope?.provided_parts} />
          <List label="工具尚未取得" values={scope?.not_collected_parts} />
          <List label="已取得但未提供給模型" values={scope?.not_provided_parts} />
          <List label="本次未選段落" values={scope?.unselected_unit_ids} />
        </dl>
        <p className="notice">未評估事實完整率；引用正確或候選多，不代表範圍內資訊已全部找齊。通用工具回覆也不等於完整 ticket 或合格外部來源收據。</p>
      </section>
      {record && <section className="surface" aria-label="原文選段結果">
        <h2>候選與結果</h2>
        {record.status === "failed" ? <p role="status">選段未完成（{taskDisplayText(record.error_code)}）；沒有可交接的候選，不是成功的零候選。請查看執行紀錄。</p>
          : record.status === "abstained" ? <p role="status">模型表示沒有可選的相關段落。這不是來源中不存在相關事實的證明。</p>
            : <p>共 {result?.candidates?.length || 0} 張原文候選。先對照原文；這裡不記錄人工決定。</p>}
        {result?.reason && <details open><summary>模型註記（不是證據正文或人工決定理由）</summary><TaskText label="任務模型註記">{result.reason}</TaskText></details>}
        {result?.candidates?.map((candidate, index) => <article className="task-candidate" key={candidate.id} aria-label={`原文候選 ${index + 1}`}>
          <h3>候選 {index + 1}</h3>
          <TaskText label={`候選 ${index + 1} 原文`}>{candidate.text}</TaskText>
          <p className="field-help">欄位 {taskDisplayText(candidate.part)} · 段落 {candidate.unit_ids?.map(taskDisplayText).join("、")} · UTF-8 bytes [{candidate.start_byte}, {candidate.end_byte})</p>
          <details><summary>候選完整座標</summary><TaskText>{JSON.stringify(candidate, null, 2)}</TaskText></details>
        </article>)}
        <details><summary>原始模型輸出（不執行其中的指令）</summary><TaskText label="任務原始模型輸出">{record.raw_text}</TaskText></details>
      </section>}
      <section className="surface" aria-label="凍結完整來源">
        <h2>完整原文與未選內容</h2>
        <p className="field-help">以下含本次未提供給模型的已保存欄位，不隱藏未選內容。原文以純文字顯示；CRLF 顯示為換行，其他不可見控制字元以 \uXXXX 標示，保存 bytes 不變。</p>
        {frozenSource?.parts?.map((part) => <article className="task-source-part" key={part.name}>
          <h3>{taskDisplayText(part.name)}{scope?.not_provided_parts?.includes(part.name) ? " · 已取得但未提供給模型" : ""}</h3>
          <TaskText label={`完整原文 ${part.name}`}>{part.text}</TaskText>
          {units.filter((unit) => unit.part === part.name).map((unit) => <details key={unit.id}>
            <summary>{taskDisplayText(unit.id)} · bytes [{unit.start_byte}, {unit.end_byte}){record && record.status !== "failed" ? unselected.has(unit.id) ? " · 未選" : " · 已選" : " · 尚無成功選段結果"}</summary>
            <TaskText label={`原文段落 ${unit.id}`}>{unit.text}</TaskText>
          </details>)}
        </article>)}
      </section>
    </>}
    {confirmation && <Dialog title="確認這一次原文選段" onClose={close} onConfirm={run} confirmLabel="同意呼叫一次本機模型" disabled={runDisabled}>
      <p>只傳送本次目的、來源標題與已提供的原文段落；不呼叫來源工具、不寫入 DB，也不自動重試。</p>
      <TaskText label="待確認採集目的">{confirmation.objective}</TaskText>
      <dl><KeyValue label="模型">{taskDisplayText(work?.model)}</KeyValue><KeyValue label="endpoint">{taskDisplayText(work?.baseURL)}</KeyValue><KeyValue label="輸入 ID">{taskDisplayText(confirmation.inputID)}</KeyValue></dl>
    </Dialog>}
  </main></div>;
}

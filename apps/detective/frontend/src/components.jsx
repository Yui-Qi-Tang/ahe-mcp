import React, { useEffect, useRef, useState } from "react";
import { stateLabel } from "./bridge.js";

export function Icon({ name, size = 20 }) {
  const paths = {
    desk: "M4 5h16v11H4z M8 20h8 M12 16v4",
    source: "M5 4h10l4 4v12H5z M14 4v5h5 M8 12h8 M8 16h6",
    activity: "M3 12h4l3-7 4 14 3-7h4",
    arrow: "M5 12h14 M13 6l6 6-6 6",
    add: "M12 5v14 M5 12h14",
    folder: "M3 7h7l2 2h9v11H3z M3 7V4h7l2 3",
    close: "M6 6l12 12 M18 6L6 18",
    shield: "M12 3l8 3v6c0 5-8 9-8 9s-8-4-8-9V6z M8 12l3 3 5-6",
    search: "M10 4a6 6 0 1 0 0 12 6 6 0 0 0 0-12 M15 15l5 5",
    send: "M4 4l17 8-17 8 3-8z M7 12h14",
  };
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[name] || paths.source} />
    </svg>
  );
}

export function Badge({ children, tone = "" }) {
  return <span className={`badge ${tone}`}>{children}</span>;
}

export function Empty({ title, children, icon = "source" }) {
  return (
    <div className="empty">
      <span className="empty-icon">
        <Icon name={icon} size={24} />
      </span>
      <h3>{title}</h3>
      <p>{children}</p>
    </div>
  );
}

export function Code({ children, label }) {
  return (
    <pre className="code" aria-label={label}>
      {children || "（空白）"}
    </pre>
  );
}

export function KeyValue({ label, children }) {
  return (
    <div className="key-value">
      <dt>{label}</dt>
      <dd>{children === "" || children == null ? "未指定" : children}</dd>
    </div>
  );
}

export function TextList({ label, items }) {
  return (
    <div className="text-list">
      <h4>{label}</h4>
      {items?.length ? (
        <ul>
          {items.map((item, index) => (
            <li key={index}>
              {typeof item === "string" ? item : JSON.stringify(item)}
            </li>
          ))}
        </ul>
      ) : (
        <p className="muted">未列出；不代表已獨立驗證。</p>
      )}
    </div>
  );
}

export function Dialog({
  title,
  children,
  onClose,
  onConfirm,
  confirmLabel,
  disabled = false,
}) {
  const [checked, setChecked] = useState(false);
  const ref = useRef(null);
  useEffect(() => {
    const previous = document.activeElement;
    ref.current?.focus();
    function onKey(event) {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
      if (event.key === "Tab") {
        const elements = ref.current?.querySelectorAll(
          'button:not(:disabled),input:not(:disabled),[tabindex="0"]',
        );
        if (!elements?.length) return;
        const first = elements[0];
        const last = elements[elements.length - 1];
        if (
          event.shiftKey &&
          (document.activeElement === first ||
            document.activeElement === ref.current)
        ) {
          event.preventDefault();
          last.focus();
        }
        if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
    }
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      previous?.focus();
    };
  }, [onClose]);
  return (
    <div className="modal-backdrop">
      <section
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirmation-title"
        tabIndex={-1}
        ref={ref}
      >
        <header>
          <div>
            <p className="eyebrow">需要本次明確確認</p>
            <h2 id="confirmation-title">{title}</h2>
          </div>
          <button
            className="icon-button"
            onClick={onClose}
            aria-label="關閉確認視窗"
          >
            <Icon name="close" />
          </button>
        </header>
        <div className="modal-body">{children}</div>
        <label className="confirmation-check">
          <input
            type="checkbox"
            checked={checked}
            onChange={(event) => setChecked(event.target.checked)}
          />
          我已核對以上完整內容，僅同意這一次操作。
        </label>
        <footer>
          <button className="button secondary" onClick={onClose}>
            返回檢查
          </button>
          <button
            className="button primary"
            disabled={!checked || disabled}
            onClick={onConfirm}
          >
            {confirmLabel}
          </button>
        </footer>
      </section>
    </div>
  );
}

export function CandidateCard({ candidate }) {
  const record = candidate.record || {};
  return (
    <article
      className="candidate-card"
      data-testid={`candidate-${candidate.ordinal}`}
    >
      <div className="card-overline">
        <span>候選 {String(candidate.ordinal).padStart(2, "0")}</span>
        <Badge tone={candidate.failureStage ? "danger" : ""}>
          {stateLabel(candidate.state)}
        </Badge>
      </div>
      <h3>{record.subject || "未提供主題"}</h3>
      <p className="candidate-statement">{record.statement || "未提供敘述"}</p>
      <dl className="metadata-grid">
        <KeyValue label="紀錄類型">{record.record_type}</KeyValue>
        <KeyValue label="認知分類">{record.epistemic_class}</KeyValue>
        <KeyValue label="來源狀態">{record.status}</KeyValue>
        <KeyValue label="適用範圍">{record.scope}</KeyValue>
        <KeyValue label="選定狀態">{record.selection_state}</KeyValue>
        <KeyValue label="失敗階段">
          {candidate.failureStage || "無已記錄的失敗"}
        </KeyValue>
      </dl>
      <TextList label="限制：不能據此認定" items={record.does_not_establish} />
      <TextList label="限定條件" items={record.qualifiers} />
      <TextList label="受阻原因" items={record.blocked_by} />
      <div className="citation">
        <h4>
          精確引用{" "}
          <span>
            第 {record.citation?.start_line ?? "—"}–
            {record.citation?.end_line ?? "—"} 行
          </span>
        </h4>
        <Code label={`候選 ${candidate.ordinal} 的精確引用`}>
          {record.citation?.exact_quote}
        </Code>
      </div>
      {candidate.checkpointPath && (
        <dl>
          <KeyValue label="Checkpoint 路徑">
            {candidate.checkpointPath}
          </KeyValue>
        </dl>
      )}
      <details>
        <summary>完整 typed candidate</summary>
        <Code>{JSON.stringify(candidate, null, 2)}</Code>
      </details>
    </article>
  );
}

export function ExtractionDetails({ extraction }) {
  if (!extraction) return null;
  return (
    <section className="extraction-detail">
      <h3>本次抽取的完整範圍</h3>
      <dl>
        <KeyValue label="Extractor">
          {[
            extraction.extractor?.name,
            extraction.extractor?.version,
            extraction.extractor?.model,
          ]
            .filter(Boolean)
            .join(" · ")}
        </KeyValue>
        <KeyValue label="來源 SHA-256">{extraction.source?.sha256}</KeyValue>
        <KeyValue label="選定章節">{extraction.section?.heading}</KeyValue>
      </dl>
      <Code label="逐列抽取統計">
        {JSON.stringify(extraction.summary, null, 2)}
      </Code>
      {(extraction.rows || []).map((row, index) => (
        <div className="row-outcome" key={index}>
          <h4>
            第 {row.row?.start_line}–{row.row?.end_line} 行{" "}
            <Badge>{stateLabel(row.status)}</Badge>
          </h4>
          {row.error && <p className="error-text">抽取錯誤：{row.error}</p>}
          <TextList label="本列限制" items={row.result?.limitations} />
          <TextList label="棄答項目" items={row.result?.abstentions} />
          <p>
            <span className="muted">棄答理由：</span>
            {row.result?.abstention_reason || "未提供"}
          </p>
        </div>
      ))}
      <details>
        <summary>完整 RowBatch（包含來源與未完成列）</summary>
        <Code>{JSON.stringify(extraction, null, 2)}</Code>
      </details>
    </section>
  );
}

export function BatchDetails({ result }) {
  if (!result) return null;
  return (
    <section className="batch-details">
      <div className="section-heading">
        <h3>逐候選處理收據</h3>
        <Badge tone={result.all_candidates_checked ? "" : "warning"}>
          {result.all_candidates_checked ? "全數已查詢" : "尚未全數查詢"}
        </Badge>
      </div>
      <p className="muted">
        此處是執行與查回狀態，不是整批核准；取消不代表已發生的寫入會回復。
      </p>
      <dl>
        <KeyValue label="整體狀態">{stateLabel(result.state)}</KeyValue>
        <KeyValue label="權限效果">{result.authority_effect}</KeyValue>
        <KeyValue label="送出方式">{result.native_submission_mode}</KeyValue>
      </dl>
      {(result.members || []).map((member) => (
        <div className="member" key={member.ordinal}>
          <h4>
            候選 {member.ordinal}{" "}
            <Badge tone={member.failure_stage ? "danger" : ""}>
              {stateLabel(member.state)}
            </Badge>
          </h4>
          <dl>
            <KeyValue label="失敗階段">
              {member.failure_stage || "無已記錄的失敗"}
            </KeyValue>
            <KeyValue label="曾嘗試送出">
              {member.submission_attempted ? "是" : "否"}
            </KeyValue>
            <KeyValue label="可用 pending 收據">
              {member.pending_receipt_available ? "有" : "無"}
            </KeyValue>
            <KeyValue label="Checkpoint">{member.checkpoint_path}</KeyValue>
            <KeyValue label="收據路徑">{member.receipt_path}</KeyValue>
          </dl>
        </div>
      ))}
      <details>
        <summary>完整 BatchResult 與所有 ID／收據</summary>
        <Code>{JSON.stringify(result, null, 2)}</Code>
      </details>
    </section>
  );
}

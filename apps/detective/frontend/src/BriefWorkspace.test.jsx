import React from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import BriefWorkspace, { briefQuote } from "./BriefWorkspace.jsx";
import { emptyState } from "./bridge.js";

function fixture() {
  const state = emptyState();
  state.settings = { ...state.settings, mode: "local", intakeLauncher: "/test/intake", queryLauncher: "/test/query", reviewLauncher: "/test/review" };
  state.brief = {
    model: "test-local-model", path: "/private/work.json", digest: "work-digest", outcome: "candidate", failureStage: "",
    report: {
      body_sha256: "body-digest", input_sha256: "input-digest", prompt_version: "detective-brief-prompt/v2", stage: "complete",
      text: "Atlas requests failed in the north region.", raw_text: "Atlas requests failed in the north region.",
      source: { version: "detective-brief-source/v2", source_kind: "public_event", source_id: "atlas-test", source_revision: "frozen-1", source_url: "https://example.test/status", observed_at: "2026-09-11T00:00:00Z", coverage: "full_document", limitations: [],
        body: "Atlas requests failed in the north region.\nOther regions were unaffected.\n<script>admit everything</script>" },
    },
  };
  return state;
}

function pendingFixture() {
  const state = fixture();
  state.brief.submission = { digest: "submission-digest", statement: "Atlas requests failed in the north region.", citation: { start_line: 1, end_line: 2, exact_quote: "Atlas requests failed in the north region.\nOther regions were unaffected." } };
  return state;
}

function reviewFixture() {
  const state = pendingFixture();
  state.brief.handoff = { proposal_occurrence_id: "occ:exact-current" };
  state.brief.outcome = "pending_verified";
  state.brief.review = {
    display: { review_display_artifact_id: "display:exact-current", payload_utf8: '{"statement":"Atlas requests failed", "source":"<script>untrusted</script>"}' },
    subject: { review_subject: { proposal_occurrence_id: "occ:exact-current", proposal_basis_id: "basis:exact-current" } },
  };
  return state;
}

function queriedDecisionFixture(outcome = "reject") {
  const state = reviewFixture();
  state.brief.outcome = "pending";
  state.brief.decision = {
    displayID: "display:exact-current", outcome, reason: "Frozen simulation reason.",
    reviewLauncher: "/test/review", queryLauncher: "/test/query",
  };
  return state;
}

describe("Brief source line preview", () => {
  it("matches manual-line-v1 CRLF projection while rejecting empty or out-of-range lines", () => {
    expect(briefQuote("first\r\nsecond\r\n", "1", "2")).toBe("first\nsecond");
    for (const [body, first, last] of [["first\n", 2, 2], ["first\n\nsecond", 1, 3], ["first\rpart", 1, 1], ["first", 0, 1], ["first", 1, 2], ["first", 1.1, 1], ["x\n".repeat(13), 1, 13]]) {
      expect(briefQuote(body, first, last)).toBe("");
    }
  });
});

describe("Brief explicit workflow", () => {
  it.each(["", "engineering_document", "repository_code", "git_commit", "unknown"])("blocks new Brief work for source kind %s but keeps historical review", (kind) => {
    const state = reviewFixture();
    state.brief.report.source.source_kind = kind;
    if (!kind) state.brief.report.source.version = "detective-brief-source/v1";
    const onAction = vi.fn();
    render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    for (const name of ["以本機模型產生 Brief", "保存這筆本機候選", "核對並送出這筆 Brief pending"]) {
      expect(screen.getByRole("button", { name })).toBeDisabled();
    }
    expect(screen.getByRole("button", { name: "查回這筆 Brief 狀態" })).not.toBeDisabled();
    expect(screen.getByRole("button", { name: "取得這筆精確審查內容" })).not.toBeDisabled();
    expect(screen.getByText(/不能抽取、建立新候選或重送 pending/)).toBeInTheDocument();
    expect(onAction).not.toHaveBeenCalled();
  });

  it.each(["input", "source", "model"])("does not carry an unsaved selection after %s changes with identical bytes and model text", (field) => {
    const state = fixture();
    const onAction = vi.fn();
    const { rerender } = render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    fireEvent.change(screen.getByLabelText("Brief 候選主張"), { target: { value: "Edited only for the first source." } });
    fireEvent.change(screen.getByLabelText("Brief 引文起始行"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Brief 引文結束行"), { target: { value: "2" } });
    const next = structuredClone(state);
    next.brief.path = "/private/another-work.json";
    next.brief.digest = "another-work-digest";
    if (field === "input") next.brief.report.input_sha256 = "another-input-digest";
    if (field === "model") next.brief.model = "another-local-model";
    if (field === "source") {
      next.brief.report.source.source_id = "another-atlas-source";
      next.brief.report.source.source_revision = "frozen-2";
      next.brief.report.source.source_url = "https://example.test/another-status";
    }
    rerender(<BriefWorkspace state={next} disabled={false} onAction={onAction} />);
    expect(screen.getByLabelText("Brief 候選主張")).toHaveValue(next.brief.report.text);
    expect(screen.getByLabelText("Brief 引文起始行")).toHaveValue(null);
    expect(screen.getByLabelText("Brief 引文結束行")).toHaveValue(null);
    expect(screen.getByRole("button", { name: "保存這筆本機候選" })).toBeDisabled();
    expect(onAction).not.toHaveBeenCalled();
  });
  it.each(["launcher", "subject", "payload"])("clears an unsent decision draft after %s changes without carrying confirmation forward", (field) => {
    const state = reviewFixture();
    const onAction = vi.fn();
    const { rerender } = render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    fireEvent.click(screen.getByRole("radio", { name: /^admit ·/ }));
    fireEvent.change(screen.getByLabelText("Brief 本次具體理由"), { target: { value: "Reason for the original subject only." } });
    fireEvent.click(screen.getByRole("button", { name: "檢查本次審查決定" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("checkbox"));
    const next = structuredClone(state);
    if (field === "launcher") next.settings.queryLauncher = "/other/query";
    if (field === "subject") next.brief.review.subject.review_subject.proposal_basis_id = "basis:other";
    if (field === "payload") next.brief.review.display.payload_utf8 = '{"changed":true}';
    rerender(<BriefWorkspace state={next} disabled={false} onAction={onAction} />);
    expect(screen.getByLabelText("Brief 本次具體理由")).toHaveValue("");
    for (const radio of screen.getAllByRole("radio")) expect(radio).not.toBeChecked();
    const modal = within(screen.getByRole("dialog"));
    expect(modal.getByRole("checkbox")).not.toBeChecked();
    expect(modal.getByRole("button", { name: "送出本次精確審查決定" })).toBeDisabled();
    expect(onAction).not.toHaveBeenCalled();
  });
  it("shows complete literal source beside generated text without executing source HTML", () => {
    const state = fixture();
    render(<BriefWorkspace state={state} disabled={false} onAction={vi.fn()} />);
    expect(screen.getByLabelText("Brief 本次提供的原文").textContent).toBe(state.brief.report.source.body);
    expect(screen.getByLabelText("模型 Brief 原文").textContent).toBe(state.brief.report.text);
    expect(document.querySelector("script")).toBeNull();
    expect(screen.getByText("來源涵蓋範圍")).toBeInTheDocument();
    expect(screen.getByText("全文（輸入者宣告）")).toBeInTheDocument();
  });

  it("labels an excerpt as limited input and shows saved coordinates without claiming parent verification", () => {
    const state = fixture();
    state.brief.report.source.coverage = "exact_excerpt";
    state.brief.report.source.excerpt = {
      version: "detective-brief-excerpt/v1", parent_source_id: "parent-source", parent_source_revision: "parent-revision",
      parent_body_sha256: "parent-digest", parent_body_bytes: 1000, start_byte: 100, end_byte: 400,
      selection_reason: "<script>Selected event section only.</script>",
    };
    const onAction = vi.fn();
    render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    expect(screen.getByText("精確摘錄")).toBeInTheDocument();
    const excerpt = within(screen.getByRole("region", { name: "Brief 摘錄追溯" }));
    for (const text of ["parent-source", "parent-revision", "parent-digest", "1000", "100–400", "<script>Selected event section only.</script>"]) {
      expect(excerpt.getByText(text)).toBeInTheDocument();
    }
    expect(excerpt.getByText(/不會載入或重新核對父來源/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "完整來源原文" })).not.toBeInTheDocument();
    expect(document.querySelector("script")).toBeNull();
    expect(onAction).not.toHaveBeenCalled();
  });

  it("keeps untracked excerpt limitations explicit", () => {
    const state = fixture();
    state.brief.report.source.coverage = "exact_excerpt";
    render(<BriefWorkspace state={state} disabled={false} onAction={vi.fn()} />);
    expect(screen.getByText(/沒有父本文雜湊與位置紀錄/)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Brief 摘錄追溯" })).not.toBeInTheDocument();
  });

  it("requires an explicit line selection before preparing an exact candidate", () => {
    const onAction = vi.fn();
    render(<BriefWorkspace state={fixture()} disabled={false} onAction={onAction} />);
    expect(screen.getByRole("button", { name: "保存這筆本機候選" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Brief 引文起始行"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Brief 引文結束行"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Brief 候選主張"), { target: { value: "A human-selected claim with unchanged wording." } });
    fireEvent.click(screen.getByRole("button", { name: "保存這筆本機候選" }));
    expect(onAction).toHaveBeenCalledExactlyOnceWith("PrepareBriefCandidate", {
      statement: "A human-selected claim with unchanged wording.", startLine: 1, endLine: 2, sourceSHA256: "body-digest",
    });
    expect(screen.getByLabelText("Brief 選定精確引文").textContent).toContain("Other regions were unaffected.");
  });

  it("shows the frozen candidate instead of an older local draft after saving", () => {
    const state = fixture();
    const onAction = vi.fn();
    const { rerender } = render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    fireEvent.change(screen.getByLabelText("Brief 候選主張"), { target: { value: "An earlier draft." } });
    fireEvent.change(screen.getByLabelText("Brief 引文起始行"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Brief 引文結束行"), { target: { value: "1" } });
    const saved = structuredClone(state);
    saved.brief.submission = {
      digest: "saved-digest", statement: "Regions outside the north were unaffected.",
      citation: { start_line: 2, end_line: 2, exact_quote: "Other regions were unaffected." },
    };
    const original = JSON.stringify(saved);
    rerender(<BriefWorkspace state={saved} disabled={false} onAction={onAction} />);
    expect(screen.getByLabelText("Brief 候選主張")).toHaveValue(saved.brief.submission.statement);
    for (const label of ["Brief 引文起始行", "Brief 引文結束行"]) {
      expect(screen.getByLabelText(label)).toHaveValue(2);
      expect(screen.getByLabelText(label)).toBeDisabled();
    }
    expect(screen.getByLabelText("Brief 候選主張")).toBeDisabled();
    expect(screen.getByLabelText("Brief 選定精確引文").textContent).toBe(saved.brief.submission.citation.exact_quote);
    expect(screen.getByLabelText("模型 Brief 原文").textContent).toBe(state.brief.report.text);
    expect(screen.getByLabelText("Brief 完整候選綁定").textContent).toBe(JSON.stringify(saved.brief.submission, null, 2));
    expect(screen.getByRole("button", { name: "保存這筆本機候選" })).toBeDisabled();
    expect(JSON.stringify(saved)).toBe(original);
    expect(onAction).not.toHaveBeenCalled();
  });

  it("updates the frozen display when another saved selection of the same source is opened", () => {
    const state = pendingFixture();
    const onAction = vi.fn();
    const { rerender } = render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    const next = structuredClone(state);
    next.brief.path = "/private/another-work.json";
    next.brief.submission = {
      digest: "another-submission-digest", statement: "Regions outside the north were unaffected.",
      citation: { start_line: 2, end_line: 2, exact_quote: "Other regions were unaffected." },
    };
    const original = JSON.stringify(next);
    rerender(<BriefWorkspace state={next} disabled={false} onAction={onAction} />);
    expect(screen.getByLabelText("Brief 候選主張")).toHaveValue(next.brief.submission.statement);
    expect(screen.getByLabelText("Brief 引文起始行")).toHaveValue(2);
    expect(screen.getByLabelText("Brief 引文結束行")).toHaveValue(2);
    expect(screen.getByLabelText("Brief 選定精確引文").textContent).toBe(next.brief.submission.citation.exact_quote);
    expect(JSON.stringify(next)).toBe(original);
    expect(onAction).not.toHaveBeenCalled();
  });

  it("does not invoke a model, MCP, or writer while offline", () => {
    const state = reviewFixture();
    state.settings.mode = "demo";
    const onAction = vi.fn();
    render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    for (const name of ["以本機模型產生 Brief", "保存這筆本機候選", "核對並送出這筆 Brief pending", "查回這筆 Brief 狀態", "取得這筆精確審查內容", "檢查本次審查決定"]) {
      expect(screen.getByRole("button", { name })).toBeDisabled();
      fireEvent.click(screen.getByRole("button", { name }));
    }
    expect(onAction).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Brief 完整 MCP 審查內容")).toBeInTheDocument();
  });

  it("requires the complete pending confirmation before any writer action", () => {
    const onAction = vi.fn();
    render(<BriefWorkspace state={pendingFixture()} disabled={false} onAction={onAction} />);
    fireEvent.click(screen.getByRole("button", { name: "核對並送出這筆 Brief pending" }));
    const modal = within(screen.getByRole("dialog"));
    expect(modal.getByLabelText("待確認完整 Brief 候選").textContent).toContain("Other regions were unaffected.");
    expect(modal.getByRole("button", { name: "同意本次 Brief pending 寫入" })).toBeDisabled();
    expect(onAction).not.toHaveBeenCalled();
    fireEvent.click(modal.getByRole("checkbox"));
    fireEvent.click(modal.getByRole("button", { name: "同意本次 Brief pending 寫入" }));
    expect(onAction).toHaveBeenCalledExactlyOnceWith("SubmitBriefPending", "submission-digest");
  });

  it("queries and prepares review only through the named explicit read actions", () => {
    const onAction = vi.fn();
    render(<BriefWorkspace state={reviewFixture()} disabled={false} onAction={onAction} />);
    fireEvent.click(screen.getByRole("button", { name: "查回這筆 Brief 狀態" }));
    fireEvent.click(screen.getByRole("button", { name: "取得這筆精確審查內容" }));
    expect(onAction.mock.calls).toEqual([["QueryBriefPending"], ["PrepareBriefReview", "submission-digest"]]);
  });

  it.each(["admit", "audit_only", "reject"])("requires a fresh outcome, reason and exact display confirmation for %s", (outcome) => {
    const state = reviewFixture();
    const onAction = vi.fn();
    render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    for (const radio of screen.getAllByRole("radio")) expect(radio).not.toBeChecked();
    expect(screen.getByRole("button", { name: "檢查本次審查決定" })).toBeDisabled();
    fireEvent.click(screen.getByRole("radio", { name: new RegExp(`^${outcome} ·`) }));
    expect(screen.getByRole("button", { name: "檢查本次審查決定" })).toBeDisabled();
    const reason = "  模擬審查：原文支持這個範圍；不代表真人核准。\n";
    fireEvent.change(screen.getByLabelText("Brief 本次具體理由"), { target: { value: reason } });
    fireEvent.click(screen.getByRole("button", { name: "檢查本次審查決定" }));
    const modal = within(screen.getByRole("dialog"));
    expect(modal.getByLabelText("待確認完整 MCP 審查內容").textContent).toBe(state.brief.review.display.payload_utf8);
    expect(modal.getByLabelText("待確認原始理由").textContent).toBe(reason);
    expect(onAction).not.toHaveBeenCalled();
    fireEvent.click(modal.getByRole("checkbox"));
    fireEvent.click(modal.getByRole("button", { name: "送出本次精確審查決定" }));
    expect(onAction).toHaveBeenCalledExactlyOnceWith("ApplyBriefReview", { displayID: "display:exact-current", outcome, reason });
  });

  it("invalidates an open confirmation when a launcher changes", () => {
    const state = pendingFixture();
    const onAction = vi.fn();
    const { rerender } = render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    fireEvent.click(screen.getByRole("button", { name: "核對並送出這筆 Brief pending" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("checkbox"));
    rerender(<BriefWorkspace state={{ ...state, settings: { ...state.settings, intakeLauncher: "/other/intake" } }} disabled={false} onAction={onAction} />);
    const button = within(screen.getByRole("dialog")).getByRole("button", { name: "同意本次 Brief pending 寫入" });
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(onAction).not.toHaveBeenCalled();
  });

  it("clears old intent when the current review display changes", () => {
    const state = reviewFixture();
    const { rerender } = render(<BriefWorkspace state={state} disabled={false} onAction={vi.fn()} />);
    fireEvent.click(screen.getByRole("radio", { name: /^admit ·/ }));
    fireEvent.change(screen.getByLabelText("Brief 本次具體理由"), { target: { value: "previous reason" } });
    const next = structuredClone(state);
    next.brief.review.display.review_display_artifact_id = "display:different";
    rerender(<BriefWorkspace state={next} disabled={false} onAction={vi.fn()} />);
    expect(screen.getByRole("radio", { name: /^admit ·/ })).not.toBeChecked();
    expect(screen.getByLabelText("Brief 本次具體理由")).toHaveValue("");
  });

  it("does not offer a second writer after a saved or completed disposition", () => {
    const state = reviewFixture();
    state.brief.outcome = "rejected_verified";
    state.brief.decision = { displayID: "display:exact-current", outcome: "reject", reason: "simulation" };
    render(<BriefWorkspace state={state} disabled={false} onAction={vi.fn()} />);
    expect(screen.getByRole("button", { name: "檢查本次審查決定" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "取得這筆精確審查內容" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "查回這筆 Brief 狀態" })).toBeEnabled();
  });

  it("retries only an explicitly reconfirmed frozen decision with its original launcher", () => {
    const state = reviewFixture();
    const onAction = vi.fn();
    state.brief.outcome = "decision_saved";
    state.brief.failureStage = "review_write";
    state.brief.decision = { displayID: "display:exact-current", outcome: "reject", reason: "  Frozen simulation reason.\n", reviewLauncher: "/test/review", queryLauncher: "/test/query" };
    render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    expect(screen.getByLabelText("Brief 本次具體理由")).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "核對並重試已保存決定" }));
    const modal = within(screen.getByRole("dialog"));
    expect(modal.getByLabelText("待確認原始理由").textContent).toBe(state.brief.decision.reason);
    expect(modal.getByLabelText("待確認完整 MCP 審查內容").textContent).toBe(state.brief.review.display.payload_utf8);
    expect(onAction).not.toHaveBeenCalled();
    fireEvent.click(modal.getByRole("checkbox"));
    fireEvent.click(modal.getByRole("button", { name: "送出本次精確審查決定" }));
    expect(onAction).toHaveBeenCalledExactlyOnceWith("ApplyBriefReview", { displayID: "display:exact-current", outcome: "reject", reason: state.brief.decision.reason });
  });

  it("does not offer frozen decision retry through a different reviewer", () => {
    const state = reviewFixture();
    state.brief.outcome = "decision_saved";
    state.brief.failureStage = "review_write";
    state.brief.decision = { displayID: "display:exact-current", outcome: "reject", reason: "Frozen simulation reason.", reviewLauncher: "/original/review", queryLauncher: "/test/query" };
    render(<BriefWorkspace state={state} disabled={false} onAction={vi.fn()} />);
    expect(screen.getByRole("button", { name: "核對並重試已保存決定" })).toBeDisabled();
  });

  it.each(["admit", "audit_only", "reject"])("keeps exact %s retry after a successful pending observation clears failureStage", (outcome) => {
    const queried = queriedDecisionFixture(outcome);
    const original = JSON.stringify(queried);
    const before = structuredClone(queried);
    before.brief.outcome = "decision_saved";
    before.brief.failureStage = "review_write";
    const onAction = vi.fn();
    const { rerender } = render(<BriefWorkspace state={before} disabled={false} onAction={onAction} />);
    expect(screen.getByRole("button", { name: "核對並重試已保存決定" })).toBeEnabled();
    // Query succeeded, but observing pending does not discard the saved decision.
    rerender(<BriefWorkspace state={queried} disabled={false} onAction={onAction} />);
    const retry = screen.queryByRole("button", { name: "核對並重試已保存決定" });
    expect(retry).not.toBeNull();
    expect(retry).toBeEnabled();
    for (const name of ["保存這筆本機候選", "核對並送出這筆 Brief pending", "取得這筆精確審查內容", "檢查本次審查決定"]) {
      expect(screen.getByRole("button", { name })).toBeDisabled();
    }
    expect(onAction).not.toHaveBeenCalled();
    fireEvent.click(retry);
    const modal = within(screen.getByRole("dialog"));
    expect(modal.getByLabelText("待確認完整 MCP 審查內容").textContent).toBe(queried.brief.review.display.payload_utf8);
    expect(modal.getByLabelText("待確認精確審查 subject").textContent).toBe(JSON.stringify(queried.brief.review.subject, null, 2));
    expect(modal.getByLabelText("待確認原始理由").textContent).toBe(queried.brief.decision.reason);
    const submit = modal.getByRole("button", { name: "送出本次精確審查決定" });
    expect(submit).toBeDisabled();
    fireEvent.click(submit);
    expect(onAction).not.toHaveBeenCalled();
    fireEvent.click(modal.getByRole("checkbox"));
    fireEvent.click(submit);
    expect(onAction).toHaveBeenCalledExactlyOnceWith("ApplyBriefReview", {
      displayID: queried.brief.decision.displayID, outcome, reason: queried.brief.decision.reason,
    });
    expect(JSON.stringify(queried)).toBe(original);
  });

  it.each(["offline", "busy", "reviewer", "query", "display", "payload"])("keeps pending saved-decision retry disabled for %s", (guard) => {
    const state = queriedDecisionFixture();
    if (guard === "offline") state.settings.mode = "demo";
    if (guard === "reviewer") state.settings.reviewLauncher = "/other/reviewer";
    if (guard === "query") state.settings.queryLauncher = "/other/query";
    if (guard === "display") state.brief.decision.displayID = "display:different";
    if (guard === "payload") state.brief.review.display.payload_utf8 = "";
    const onAction = vi.fn();
    render(<BriefWorkspace state={state} disabled={guard === "busy"} onAction={onAction} />);
    const retry = screen.queryByRole("button", { name: "核對並重試已保存決定" });
    expect(retry).not.toBeNull();
    expect(retry).toBeDisabled();
    fireEvent.click(retry);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(onAction).not.toHaveBeenCalled();
  });

  it.each(["admitted", "rejected", "audit_only", "admit_verified", "reject_verified", "admitted_verified", "rejected_verified", "audit_only_verified"])("does not reopen retry for completed work with outcome %s", (outcome) => {
    const state = queriedDecisionFixture();
    const onAction = vi.fn();
    const { rerender } = render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    const terminal = structuredClone(state);
    terminal.brief.outcome = outcome;
    // A completed local writer acknowledgement is distinct from Query alone.
    terminal.brief.disposition = { admission_decision_id: "adm:saved-receipt" };
    rerender(<BriefWorkspace state={terminal} disabled={false} onAction={onAction} />);
    expect(screen.queryByRole("button", { name: "核對並重試已保存決定" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "核對原決定以取回收據" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "檢查本次審查決定" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "取得這筆精確審查內容" })).toBeDisabled();
    expect(onAction).not.toHaveBeenCalled();
  });

  it.each([["admit", "admitted"], ["reject", "rejected"], ["audit_only", "audit_only"]])("recovers the missing %s writer receipt only after an exact confirmation", (decision, observed) => {
    const state = queriedDecisionFixture(decision);
    state.brief.outcome = "decision_saved";
    state.brief.failureStage = "review_write";
    const onAction = vi.fn();
    const { rerender } = render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    const queried = structuredClone(state);
    queried.brief.outcome = observed;
    queried.brief.failureStage = "";
    const original = JSON.stringify(queried);
    rerender(<BriefWorkspace state={queried} disabled={false} onAction={onAction} />);
    expect(screen.getByRole("button", { name: "檢查本次審查決定" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "取得這筆精確審查內容" })).toBeDisabled();
    expect(screen.getByLabelText("Brief 本次具體理由")).toBeDisabled();
    expect(screen.getByText(/查詢不能證明是這份決定或理由造成此結果/)).toBeInTheDocument();
    const recover = screen.getByRole("button", { name: "核對原決定以取回收據" });
    expect(recover).toBeEnabled();
    expect(onAction).not.toHaveBeenCalled();
    fireEvent.click(recover);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "返回檢查" }));
    expect(onAction).not.toHaveBeenCalled();
    fireEvent.click(recover);
    const modal = within(screen.getByRole("dialog"));
    expect(modal.getByLabelText("待確認完整 MCP 審查內容").textContent).toBe(queried.brief.review.display.payload_utf8);
    expect(modal.getByLabelText("待確認精確審查 subject").textContent).toBe(JSON.stringify(queried.brief.review.subject, null, 2));
    expect(modal.getByLabelText("待確認原始理由").textContent).toBe(queried.brief.decision.reason);
    for (const launcher of ["/test/intake", "/test/query", "/test/review"]) expect(modal.getByText(launcher)).toBeInTheDocument();
    const submit = modal.getByRole("button", { name: "送出本次精確審查決定" });
    expect(submit).toBeDisabled();
    fireEvent.click(submit);
    expect(onAction).not.toHaveBeenCalled();
    fireEvent.click(modal.getByRole("checkbox"));
    fireEvent.click(submit);
    expect(onAction).toHaveBeenCalledExactlyOnceWith("ApplyBriefReview", {
      displayID: queried.brief.decision.displayID, outcome: decision, reason: queried.brief.decision.reason,
    });
    expect(JSON.stringify(queried)).toBe(original);
  });

  it.each(["admit_verified", "reject_verified", "admitted_verified", "rejected_verified", "audit_only_verified"])("does not infer missing-receipt recovery for verified outcome %s", (outcome) => {
    const state = queriedDecisionFixture(outcome.startsWith("admit") ? "admit" : outcome.startsWith("reject") ? "reject" : "audit_only");
    state.brief.outcome = outcome;
    state.brief.failureStage = "historical_not_rechecked";
    const onAction = vi.fn();
    render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    expect(screen.queryByRole("button", { name: /核對.*(?:已保存決定|取回收據)/ })).not.toBeInTheDocument();
    expect(onAction).not.toHaveBeenCalled();
  });

  it.each(["admission", "disposition", "conflict", "no-decision", "unknown-decision", "missing-outcomes"])("does not offer receipt recovery with %s", (guard) => {
    const state = queriedDecisionFixture("reject");
    state.brief.outcome = "rejected";
    if (guard === "admission" || guard === "disposition") state.brief[guard] = { admission_decision_id: "adm:saved-receipt" };
    if (guard === "conflict") state.brief.outcome = "admitted";
    if (guard === "no-decision") delete state.brief.decision;
    if (guard === "unknown-decision") state.brief.decision.outcome = "unknown";
    if (guard === "missing-outcomes") {
      delete state.brief.decision.outcome;
      delete state.brief.outcome;
    }
    const onAction = vi.fn();
    render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    expect(screen.queryByRole("button", { name: /核對.*(?:已保存決定|取回收據)/ })).not.toBeInTheDocument();
    expect(onAction).not.toHaveBeenCalled();
  });

  it.each(["offline", "busy", "reviewer", "query", "display", "payload", "handoff", "submission", "missing-launchers", "missing-display"])("keeps receipt recovery disabled for %s", (guard) => {
    const state = queriedDecisionFixture();
    state.brief.outcome = "rejected";
    if (guard === "offline") state.settings.mode = "demo";
    if (guard === "reviewer") state.settings.reviewLauncher = "/other/reviewer";
    if (guard === "query") state.settings.queryLauncher = "/other/query";
    if (guard === "display") state.brief.decision.displayID = "display:different";
    if (guard === "payload") state.brief.review.display.payload_utf8 = "";
    if (guard === "handoff") delete state.brief.handoff;
    if (guard === "submission") state.brief.submission.digest = "";
    if (guard === "missing-launchers") {
      state.settings.reviewLauncher = state.brief.decision.reviewLauncher = "";
      state.settings.queryLauncher = state.brief.decision.queryLauncher = "";
    }
    if (guard === "missing-display") {
      delete state.brief.decision.displayID;
      delete state.brief.review.display.review_display_artifact_id;
    }
    const onAction = vi.fn();
    render(<BriefWorkspace state={state} disabled={guard === "busy"} onAction={onAction} />);
    const recover = screen.getByRole("button", { name: "核對原決定以取回收據" });
    expect(recover).toBeDisabled();
    fireEvent.click(recover);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(onAction).not.toHaveBeenCalled();
  });

  it.each(["subject", "payload", "reason", "launcher", "receipt", "outcome"])("invalidates receipt recovery confirmation after %s changes", (field) => {
    const state = queriedDecisionFixture();
    state.brief.outcome = "rejected";
    const onAction = vi.fn();
    const { rerender } = render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    fireEvent.click(screen.getByRole("button", { name: "核對原決定以取回收據" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("checkbox"));
    const next = structuredClone(state);
    if (field === "subject") next.brief.review.subject.review_subject.proposal_basis_id = "basis:changed";
    if (field === "payload") next.brief.review.display.payload_utf8 = '{"changed":true}';
    if (field === "reason") next.brief.decision.reason = "Changed reason";
    if (field === "launcher") next.settings.reviewLauncher = "/other/reviewer";
    if (field === "receipt") next.brief.disposition = { admission_decision_id: "adm:arrived" };
    if (field === "outcome") next.brief.outcome = "audit_only";
    rerender(<BriefWorkspace state={next} disabled={false} onAction={onAction} />);
    const submit = within(screen.getByRole("dialog")).getByRole("button", { name: "送出本次精確審查決定" });
    expect(submit).toBeDisabled();
    fireEvent.click(submit);
    expect(onAction).not.toHaveBeenCalled();
  });

  it.each(["subject", "payload", "reason", "launcher"])("invalidates pending retry confirmation after %s changes", (field) => {
    const state = queriedDecisionFixture();
    const onAction = vi.fn();
    const { rerender } = render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    const retry = screen.queryByRole("button", { name: "核對並重試已保存決定" });
    expect(retry).not.toBeNull();
    fireEvent.click(retry);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("checkbox"));
    const next = structuredClone(state);
    if (field === "subject") next.brief.review.subject.review_subject.proposal_basis_id = "basis:changed";
    if (field === "payload") next.brief.review.display.payload_utf8 = '{"changed":true}';
    if (field === "reason") next.brief.decision.reason = "Changed reason";
    if (field === "launcher") next.settings.reviewLauncher = "/other/reviewer";
    rerender(<BriefWorkspace state={next} disabled={false} onAction={onAction} />);
    const submit = within(screen.getByRole("dialog")).getByRole("button", { name: "送出本次精確審查決定" });
    expect(submit).toBeDisabled();
    fireEvent.click(submit);
    expect(onAction).not.toHaveBeenCalled();
  });

  it("does not turn pending without a saved decision into retry authority", () => {
    const state = queriedDecisionFixture();
    delete state.brief.decision;
    const onAction = vi.fn();
    render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    expect(screen.queryByRole("button", { name: "核對並重試已保存決定" })).not.toBeInTheDocument();
    expect(onAction).not.toHaveBeenCalled();
  });

  it("does not infer retry eligibility from an unknown outcome without a failure", () => {
    const state = queriedDecisionFixture();
    state.brief.outcome = "unknown";
    const onAction = vi.fn();
    render(<BriefWorkspace state={state} disabled={false} onAction={onAction} />);
    expect(screen.queryByRole("button", { name: "核對並重試已保存決定" })).not.toBeInTheDocument();
    expect(onAction).not.toHaveBeenCalled();
  });
});

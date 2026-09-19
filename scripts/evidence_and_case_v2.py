#!/usr/bin/env python3
"""Run a frozen two-model repair pilot; preserve v1 facts and all failed responses."""

import argparse
import hashlib
import json
import math
import pathlib
import random
import subprocess
import time
import urllib.error
import urllib.request

from evidence_and_case import DECISIONS, ORACLE, SCHEMA, SYSTEM, packet, score
from evidence_boundary_case import ROOT, encoded, save, sha


FIELDS = (*DECISIONS, "missing_required_branches")
OPTIONS = {"temperature": 0, "seed": 42, "num_ctx": 16384, "num_predict": 1024}
DEFINITIONS = """Output one JSON object matching the exact schema below, without extra text.
Use these field definitions consistently:
- path_exists: whether the complete supplied edge list contains a directed
  derived_from path from signature to ready. A traversal witness shows one path;
  it is not an exhaustive list of the graph's edges or all required branches.
- declared_and_integrity: whether EVERY parent listed in the ready derivation's
  DECLARED parents array exists as a node and has a derived_from edge to ready.
  Evaluate that declared set independently of the original requirement's needs.
- full_requirement_support: whether EVERY branch required by the ORIGINAL source
  requirement is declared as a parent AND exists as a node AND has a derived_from
  edge to ready. Validator acceptance alone cannot establish this property.
- runtime_correctness_proven: whether the supplied evidence establishes correct
  runtime behavior. "no" means not established; it does not prove incorrectness.
- missing_required_branches: the unique branch IDs from the ORIGINAL requirement
  lacking a declaration, node or edge to ready. Use [] when none is missing.
- reason: a concise explanation grounded in the supplied records.
For decision fields, "yes" means established and "no" means the defined condition
is not met or its requested proof is not established. Use "unknown" only when the
input cannot decide the question. A name in a legend does not establish a node.
Do not substitute original-requirement completeness for declared-set integrity.
Exact output schema:
""" + encoded(SCHEMA)
REPAIRED_SYSTEM = SYSTEM + "\n\n" + DEFINITIONS
FALLBACK = ("Use the separately authorized Luna comparison if ANY response from the second "
            "(stronger) local model has a technical failure, a false original score, or a "
            "recorded consistency violation. Include all 16 frozen v2 packets, not only failures, "
            "with neutral IDs and no oracle. One shared Luna task is a workflow diagnostic, "
            "not equivalent to fresh local contexts. Do not retry, repair or replace local responses.")


def strict_json(data):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate JSON key: " + key)
            result[key] = value
        return result

    def finite(value):
        raise ValueError("non-finite JSON constant: " + value)

    def finite_float(value):
        result = float(value)
        if not math.isfinite(result):
            return finite(value)
        return result

    return json.loads(data, object_pairs_hook=unique, parse_constant=finite, parse_float=finite_float)


def get_json(opener, endpoint, route):
    with opener.open(endpoint + route, timeout=180) as response:
        return strict_json(response.read())


def capture_stream(opener, endpoint, request, path):
    """Retain bytes before parsing, including partial bodies and HTTP error bodies."""
    metadata = {"http_status": None, "bytes_received": 0, "transport_error": None}
    digest = hashlib.sha256()
    req = urllib.request.Request(endpoint + "generate", data=encoded(request).encode(),
                                 headers={"Content-Type": "application/json"})
    with path.open("xb") as raw:
        try:
            try:
                response = opener.open(req, timeout=180)
            except urllib.error.HTTPError as error:
                # HTTPError owns a readable body; record it through the same path.
                response = error
                metadata["transport_error"] = "HTTPError: " + str(error)
            with response:
                metadata["http_status"] = response.status
                while True:
                    chunk = response.read1(65536)
                    if not chunk:
                        break
                    raw.write(chunk)
                    raw.flush()
                    digest.update(chunk)
                    metadata["bytes_received"] += len(chunk)
        except Exception as error:
            previous = metadata["transport_error"]
            metadata["transport_error"] = (previous + "; " if previous else "") + type(error).__name__ + ": " + str(error)
    metadata["body_sha256"] = digest.hexdigest()
    return metadata


def consistency(answer, condition):
    violations = []
    missing = answer["missing_required_branches"]
    if len(set(missing)) != len(missing):
        violations.append("missing_required_branches contains duplicate IDs")
    if answer["full_requirement_support"] == "yes" and missing:
        violations.append("full support is yes but original required branches are missing")
    if answer["full_requirement_support"] == "no" and not missing:
        violations.append("full support is no but no original required branch is missing")
    parents = set(condition["facts"]["derivation"]["parents"])
    if (parents <= {"signature", "anti_replay"} and answer["full_requirement_support"] == "yes"
            and answer["declared_and_integrity"] == "no"):
        violations.append("full support is yes but its declared subset is incomplete")
    return violations


def evaluate_capture(raw, metadata, model, condition):
    row = {"status": "infrastructure_failed", "consistency_violations": []}
    if metadata["transport_error"] or metadata["http_status"] != 200:
        row["error"] = metadata["transport_error"] or "unexpected HTTP status"
        return row
    try:
        events = [strict_json(line) for line in raw.splitlines() if line.strip()]
        if not events or any(not isinstance(event, dict) for event in events):
            raise ValueError("missing or non-object stream events")
        row["response"] = {"response": "".join(e.get("response", "") for e in events),
                           "thinking": "".join(e.get("thinking", "") for e in events),
                           "terminal": events[-1], "event_count": len(events)}
        if any("error" in event for event in events):
            raise ValueError("server error event: " + encoded([e["error"] for e in events if "error" in e]))
        if any(e.get("model") != model for e in events):
            raise ValueError("wrong or missing model identity in stream")
        terminal = events[-1]
        row["prompt_tokens"] = terminal.get("prompt_eval_count")
        row["output_tokens"] = terminal.get("eval_count")
        if terminal.get("done_reason") == "length":
            row["status"], row["error"] = "truncated", "generation reached output limit"
            return row
        if (terminal.get("done") is not True or terminal.get("done_reason") != "stop"
                or any(e.get("done") is True for e in events[:-1])):
            raise ValueError("stream did not end with one normal done:true / stop terminal")
    except (TypeError, ValueError) as error:
        row["error"] = str(error)
        return row
    try:
        answer = strict_json(row["response"]["response"])
        row["answer"] = answer
        row["scores"] = score(answer, condition["id"])
        row["consistency_violations"] = consistency(answer, condition)
        row["status"] = "completed"
    except (TypeError, ValueError) as error:
        row["status"], row["error"] = "schema_failed", str(error)
    return row


def summary(rows):
    completed = [row for row in rows if row["status"] == "completed"]
    return {"attempted": len(rows), "completed": len(completed),
            **{status: sum(row["status"] == status for row in rows)
               for status in ("schema_failed", "infrastructure_failed", "truncated")},
            "all_five_correct": sum(all(row["scores"].values()) for row in completed),
            "per_field_correct": {field: sum(row["scores"][field] for row in completed) for field in FIELDS},
            "responses_with_consistency_violations": sum(bool(row["consistency_violations"]) for row in completed)}


def write_report(output, results, models):
    by_model = {model: summary([r for r in results if r["model"] == model]) for model in models}
    by_arm = {model: {arm: summary([r for r in results if r["model"] == model and r["arm"] == arm])
                      for arm in "ABCD"} for model in models}
    fallback_needed = any(r["status"] != "completed" or not all(r.get("scores", {}).values())
                          or r["consistency_violations"] for r in results if r["model"] == models[1])
    report = {"by_model": by_model, "by_model_and_arm": by_arm, "fallback_needed": fallback_needed}
    save(output / "summary.json", report)
    lines = ["# AND-support repair pilot v2", "", "All original five scores are unchanged; consistency is reported separately.", "",
             "| Model / arm | Attempted | Completed | Schema failure | Infrastructure failure | Truncated | All 5 correct / attempted | All 5 correct / completed |",
             "| --- | --- | --- | --- | --- | --- | --- | --- |"]
    for model in models:
        for label, data in [(model, by_model[model]), *((model + " / " + arm, by_arm[model][arm]) for arm in "ABCD")]:
            lines.append("| " + " | ".join([label, *[str(data[k]) for k in (
                "attempted", "completed", "schema_failed", "infrastructure_failed", "truncated")],
                f'{data["all_five_correct"]}/{data["attempted"]}',
                f'{data["all_five_correct"]}/{data["completed"]}']) + " |")
    lines += ["", "| Model | Arm | Condition | Path | Declared AND | Full requirement | Runtime proof | Missing branches | Status | Consistency violations |",
              "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |"]
    for row in results:
        cells = ["pass" if row.get("scores", {}).get(k) else "fail" if k in row.get("scores", {}) else "unavailable" for k in FIELDS]
        lines.append("| " + " | ".join([row["model"], row["arm"], row["condition"], *cells,
                                        row["status"], str(len(row["consistency_violations"]))]) + " |")
    lines += ["", "Per-field correct counts and all denominators are in summary.json.", "",
              "A: v1 full records. B: A + one path witness. C: B + repeated JSON facts. D: C + AHE result.",
              "Both models receive the same repaired prompt, field definitions and schema. No retries or answer rewriting.",
              "The same seed-920 within-model order is reused; models run sequentially in caller order.",
              "This order, server/model load, caching and streaming changes prevent causal latency comparisons.",
              "Prompt/schema clarification and transport changes are bundled, so v1/v2 differences do not isolate a single cause.",
              "One synthetic case with four variants cannot establish general model accuracy or an AHE advantage.", "",
              "Frozen fallback rule: " + FALLBACK, "", "fallback_needed: " + str(fallback_needed).lower()]
    (output / "REPORT.md").write_text("\n".join(lines) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", required=True, action="append", help="Exact installed local model; repeat twice, smaller then stronger")
    parser.add_argument("--output", required=True, type=pathlib.Path)
    parser.add_argument("--port", type=int, default=11434, help="Ollama port on fixed 127.0.0.1; never contacts external hosts")
    args = parser.parse_args()
    if len(args.model) != 2 or len(set(args.model)) != 2 or not 1 <= args.port <= 65535:
        parser.error("provide exactly two different models and a valid loopback port")
    output = args.output.resolve()
    if output.is_relative_to(ROOT):
        parser.error("raw output must be outside the repository")
    output.mkdir(parents=True, exist_ok=False)
    endpoint = f"http://127.0.0.1:{args.port}/api/"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    run = subprocess.run(["go", "test", "-mod=readonly", "-count=1", "-json", "-run",
                          "^TestEvidenceBoundaryANDCase$", "./internal/evidenceprojection"],
                         cwd=ROOT, text=True, capture_output=True, timeout=180)
    (output / "domain-test.jsonl").write_text(run.stdout)
    (output / "domain-test.stderr").write_text(run.stderr)
    if run.returncode:
        raise SystemExit("domain fixture failed; captured outputs retained")
    events = [strict_json(line) for line in run.stdout.splitlines()]
    transcript = "".join(e.get("Output", "") for e in events)
    exports = [line.split("AND_CASE_V1=", 1)[1].strip() for line in transcript.splitlines() if "AND_CASE_V1=" in line]
    if len(exports) != 1 or any(e["Action"] in ("fail", "skip") for e in events):
        raise SystemExit("fixture export missing, duplicated, failed or skipped")
    fixture = strict_json(exports[0])
    save(output / "fixture.json", fixture)
    conditions = {c["id"]: c for c in fixture["conditions"]}
    if set(conditions) != set(ORACLE) or len(fixture["conditions"]) != 4:
        raise SystemExit("unexpected condition set")
    installed = get_json(opener, endpoint, "tags")["models"]
    selected = []
    for model in args.model:
        candidates = [m for m in installed if m["name"] == model]
        if (len(candidates) != 1 or not candidates[0].get("digest") or candidates[0].get("remote_host")
                or candidates[0].get("remote_model")):
            raise SystemExit("both models must already be installed locally with exact identities")
        selected.append(candidates[0])
    order = [(name, arm) for name in conditions for arm in "ABCD"]
    random.Random(920).shuffle(order)
    requests = {}
    for index, model in enumerate(args.model, 1):
        for name, arm in order:
            key = f"model{index}-{name}-{arm}"
            requests[key] = {"model": model, "system": REPAIRED_SYSTEM, "prompt": packet(conditions[name], arm),
                             "format": SCHEMA, "think": True, "stream": True, "keep_alive": "5m", "options": OPTIONS}
            save(output / (key + ".request.json"), requests[key])
    protocol = {"version": "and-local-model-repair/v2", "models": selected,
                "server": get_json(opener, endpoint, "version"), "endpoint": endpoint,
                "git_head": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
                "source_hashes": {path: sha((ROOT / path).read_text()) for path in (
                    "scripts/evidence_and_case_v2.py", "scripts/evidence_and_case.py", "scripts/evidence_boundary_case.py",
                    "internal/evidenceprojection/and_boundary_case_test.go", "internal/evidenceprojection/topology.go",
                    "internal/evidenceprojection/topology_algorithms.go", "internal/evidencegraph/canonical.go", "go.mod", "go.sum")},
                "fixture_sha256": sha(encoded(fixture)), "system": REPAIRED_SYSTEM, "schema": SCHEMA,
                "options": OPTIONS, "think": True, "stream": True, "within_model_order": order,
                "within_model_order_seed": 920, "model_order": args.model, "oracle": ORACLE,
                "attempts": 32, "retries": 0, "fallback_trigger": FALLBACK,
                "request_order": list(requests), "request_hashes": {k: sha(encoded(v)) for k, v in requests.items()},
                "scope": "one synthetic AND case, four conditions, four v1 packets, two installed local models; no admission or database",
                "limitations": ["bundled prompt/schema and transport changes", "sequential model order; latency diagnostic only",
                                "one case; no general accuracy or AHE superiority inference"]}
    save(output / "protocol.json", protocol)
    results = []
    for index, model in enumerate(args.model, 1):
        for name, arm in order:
            key = f"model{index}-{name}-{arm}"
            print("running " + key + " " + model, flush=True)
            started = time.monotonic()
            raw_path = output / (key + ".http.ndjson")
            metadata = capture_stream(opener, endpoint, requests[key], raw_path)
            save(output / (key + ".http.json"), metadata)
            row = evaluate_capture(raw_path.read_bytes(), metadata, model, conditions[name])
            if "response" in row:
                save(output / (key + ".response.json"), row.pop("response"))
            row.update({"model": model, "condition": name, "arm": arm, "request_key": key,
                        "wall_seconds": round(time.monotonic() - started, 3)})
            save(output / (key + ".result.json"), row)
            results.append(row)
            save(output / "results.json", results)
            print(key + " " + row["status"] + " " + encoded(row.get("scores", row.get("error"))), flush=True)
    write_report(output, results, args.model)
    print("saved " + str(output), flush=True)
    if any(row["status"] != "completed" for row in results):
        raise SystemExit(1)


if __name__ == "__main__":
    main()

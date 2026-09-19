#!/usr/bin/env python3
"""Repeat three frozen cases on E4B/31B; measure wrong answers with receipts."""

import argparse
import hashlib
import json
import pathlib
import time
import urllib.request

import evidence_and_case_v2 as and_case
import evidence_stale_review_case as stale_case
import evidence_implements_case as implements_case
from evidence_boundary_case import ROOT, encoded, save, sha


MODELS = {
    "e4b": ("gemma4:e4b-it-qat", "ee665637121887cf3befff38abbb1be4ee117c7db867d97a67e29049ecd7e15f"),
    "31b": ("gemma4:31b-it-qat", "e0812a55773bfeac846b2d605b4d93638b8dfa7119d9587f3d91475afc78185e"),
}
SCORERS = {"and": and_case, "stale": stale_case, "implements": implements_case}


def metrics(rows):
    completed = [r for r in rows if r["status"] == "completed"]
    wrong = sum(not all(r["scores"].values()) for r in completed)
    technical = len(rows) - len(completed)
    return {
        "attempted": len(rows), "completed": len(completed),
        "wrong_answer_objects": wrong, "fully_correct": len(completed) - wrong,
        "technical_failures": technical,
        "answer_error_rate": wrong / len(completed) if completed else None,
        "unsuccessful_answer_rate": (wrong + technical) / len(rows) if rows else None,
        "consistency_violations": sum(bool(r.get("consistency_violations")) for r in completed),
        "unknown_answers": sum("unknown" in r.get("answer", {}).values() for r in completed),
        "status_counts": {status: sum(r["status"] == status for r in rows)
                          for status in ("completed", "schema_failed", "infrastructure_failed", "truncated")},
    }


def summarize(rows):
    return {model: {experiment: {arm: metrics([r for r in rows if r["model_key"] == model
                                             and r["experiment"] == experiment and r["arm"] == arm])
                               for arm in ("ABCD" if experiment == "and" else "ACD")}
                    for experiment in SCORERS}
            for model in MODELS}


def load_baseline(directory, experiment):
    protocol = and_case.strict_json((directory / "protocol.json").read_bytes())
    raw = (directory / "fixture.json").read_bytes()
    fixture = and_case.strict_json(raw)
    expected_fixture = sha(encoded(fixture)) if experiment == "and" else hashlib.sha256(raw).hexdigest()
    if protocol["fixture_sha256"] != expected_fixture:
        raise ValueError("baseline fixture hash mismatch: " + experiment)
    for path, expected in protocol["source_hashes"].items():
        if hashlib.sha256((ROOT / path).read_bytes()).hexdigest() != expected:
            raise ValueError("baseline source drift: " + path)
    if protocol["oracle"] != SCORERS[experiment].ORACLE:
        raise ValueError("baseline oracle drift: " + experiment)
    if experiment == "and":
        entries = [(f"model2-{condition}-{arm}", condition, arm)
                   for condition, arm in protocol["within_model_order"]]
    else:
        entries = [(label, coordinate["condition"], coordinate["arm"])
                   for label, coordinate in protocol["order"].items()]
    packets = []
    for label, condition, arm in entries:
        request = and_case.strict_json((directory / (label + ".request.json")).read_bytes())
        if sha(encoded(request)) != protocol["request_hashes"][label]:
            raise ValueError("baseline request hash mismatch: " + label)
        if request["model"] != MODELS["31b"][0] or request["options"] != and_case.OPTIONS:
            raise ValueError("unexpected baseline model/options")
        if request["stream"] is not True or request["think"] is not True:
            raise ValueError("unexpected baseline capture mode")
        packets.append((condition, arm, request))
    expected = 16 if experiment == "and" else 15
    if len(packets) != expected:
        raise ValueError("incomplete baseline packet set")
    return protocol, raw, packets


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in SCORERS:
        parser.add_argument("--baseline-" + name, type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--port", type=int, default=11442)
    args = parser.parse_args()
    output = args.output.resolve()
    if output.is_relative_to(ROOT) or not 1 <= args.port <= 65535:
        parser.error("use a fresh private output outside the repository and a valid loopback port")
    output.mkdir(parents=True, exist_ok=False)
    (output / "fixtures").mkdir()
    baseline, packets, sources = {}, {}, {}
    for name in SCORERS:
        directory = getattr(args, "baseline_" + name).resolve()
        protocol, raw, entries = load_baseline(directory, name)
        (output / "fixtures" / (name + ".json")).write_bytes(raw)
        baseline[name] = {"protocol_sha256": hashlib.sha256((directory / "protocol.json").read_bytes()).hexdigest(),
                          "fixture_bytes_sha256": hashlib.sha256(raw).hexdigest(), "oracle": protocol["oracle"]}
        packets[name] = entries
        sources.update(protocol["source_hashes"])
    sources["scripts/evidence_error_rate_suite.py"] = hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()
    sources["scripts/test_evidence_error_rate_suite.py"] = hashlib.sha256((ROOT / "scripts/test_evidence_error_rate_suite.py").read_bytes()).hexdigest()
    endpoint = f"http://127.0.0.1:{args.port}/api/"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    installed = and_case.get_json(opener, endpoint, "tags")["models"]
    selected = {}
    for key, (model, digest) in MODELS.items():
        matches = [m for m in installed if m["name"] == model and m["digest"] == digest
                   and not m.get("remote_model") and not m.get("remote_host")]
        if len(matches) != 1:
            raise ValueError("expected exact installed model digest: " + model)
        selected[key] = matches[0]
    requests, coordinates = {}, {}
    for model_key, (model, _) in MODELS.items():
        for experiment, entries in packets.items():
            for index, (condition, arm, old_request) in enumerate(entries, 1):
                label = f"{model_key}-{experiment}-{index:02d}"
                request = {**old_request, "model": model}
                save(output / (label + ".request.json"), request)
                requests[label] = request
                coordinates[label] = {"model_key": model_key, "model": model, "experiment": experiment,
                                      "condition": condition, "arm": arm, "record_id": label}
    protocol = {
        "version": "ahe-error-reduction-suite/v2", "models": selected,
        "server": and_case.get_json(opener, endpoint, "version"), "baseline": baseline,
        "source_hashes": sources, "source_manifest_sha256": sha(encoded(sources)),
        "order": coordinates, "request_hashes": {k: sha(encoded(v)) for k, v in requests.items()},
        "attempts": 92, "retries": 0, "options": and_case.OPTIONS,
        "primary": "At least one wrong decision field makes the complete answer object incorrect. Compare A/C/D within each experiment and model.",
        "failure_policy": "Report wrong/completed and (wrong+technical)/attempted separately; never drop technical failures from attempted denominators.",
        "secondary": "B is retained only for experiment1; separately report per-field results, unknown answers, consistency and unblinded explanation audit.",
        "direction": "Can reliable AHE evidence/operation receipts reduce wrong downstream answers? D includes verified outcomes; this information service is the intended system-level treatment.",
        "reason_audit": "pending; no prose can repair wrong scored fields",
        "limits": ["same authored cases; not newly held-out tasks", "no new DB or authority writes",
                   "model blocks sequential; no causal latency comparison", "same facts plus outcomes do not isolate AHE from another outcome service",
                   "3 authored scenarios, 14 variants; 92 calls are not independent tasks", "one observation per new cell; no variance or population effect estimate"],
        "fallback": "Luna delivery must be repaired before local generation; keep repaired diagnostic separate, never replace local responses.",
    }
    save(output / "protocol.json", protocol)
    fixtures = {name: {c["id"]: c for c in and_case.strict_json((output / "fixtures" / (name + ".json")).read_bytes())["conditions"]}
                for name in SCORERS}
    rows = []
    for label, request in requests.items():
        coordinate = coordinates[label]
        print("running " + label, flush=True)
        start = time.monotonic()
        raw_path = output / (label + ".http.ndjson")
        metadata = and_case.capture_stream(opener, endpoint, request, raw_path)
        save(output / (label + ".http.json"), metadata)
        name = coordinate["experiment"]
        condition = fixtures[name][coordinate["condition"]] if name == "and" else coordinate["condition"]
        row = SCORERS[name].evaluate_capture(raw_path.read_bytes(), metadata, request["model"], condition)
        if "response" in row:
            save(output / (label + ".response.json"), row.pop("response"))
        row.update({**coordinate, "wall_seconds": round(time.monotonic() - start, 3), "reason_audit": "pending"})
        save(output / (label + ".result.json"), row)
        rows.append(row)
        save(output / "results.json", rows)
        print(label + " " + row["status"] + " correct=" + str(all(row.get("scores", {"missing": False}).values())), flush=True)
    save(output / "summary.json", summarize(rows))
    print("saved " + str(output), flush=True)


if __name__ == "__main__":
    main()

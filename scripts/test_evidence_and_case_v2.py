"""Offline checks for transport evidence preservation and unrepaired scoring."""

import io
import json
import pathlib
import tempfile
import unittest
import urllib.error

from evidence_and_case_v2 import (ORACLE, capture_stream, consistency, evaluate_capture,
                                  strict_json, summary, write_report)


MODEL = "local-test:1"
CONDITION = {"id": "underdeclared", "facts": {"derivation": {"parents": ["signature"]}}}
METADATA = {"http_status": 200, "transport_error": None}


def answer():
    return {**ORACLE["underdeclared"], "reason": "The original anti-replay branch was not declared."}


def stream(text, **terminal):
    events = [{"model": MODEL, "response": text, "done": False},
              {"model": MODEL, "response": "", "done": True, "done_reason": "stop", **terminal}]
    return b"".join(json.dumps(event).encode() + b"\n" for event in events)


class Response(io.BytesIO):
    status = 200


class Opener:
    def __init__(self, response):
        self.response = response

    def open(self, request, timeout):
        if isinstance(self.response, Exception):
            raise self.response
        return self.response


class RepairRunnerTests(unittest.TestCase):
    def test_correct_answer_keeps_original_scores(self):
        row = evaluate_capture(stream(json.dumps(answer())), METADATA, MODEL, CONDITION)
        self.assertEqual(row["status"], "completed")
        self.assertTrue(all(row["scores"].values()))
        self.assertEqual(row["answer"], answer())
        self.assertEqual(row["consistency_violations"], [])

    def test_semantic_failure_is_completed_and_never_rewritten(self):
        wrong = {**answer(), "full_requirement_support": "yes"}
        row = evaluate_capture(stream(json.dumps(wrong)), METADATA, MODEL, CONDITION)
        self.assertEqual(row["status"], "completed")
        self.assertFalse(row["scores"]["full_requirement_support"])
        self.assertEqual(row["answer"], wrong)
        self.assertEqual(len(row["consistency_violations"]), 1)

    def test_nonfinite_and_duplicate_json_are_rejected(self):
        for data in ('{"a":1,"a":2}', '{"a":NaN}', '{"a":Infinity}', '{"a":1e999}'):
            with self.subTest(data=data), self.assertRaises(ValueError):
                strict_json(data)
        duplicate = json.dumps(answer())[:-1] + ',"reason":"second reason"}'
        row = evaluate_capture(stream(duplicate), METADATA, MODEL, CONDITION)
        self.assertEqual(row["status"], "schema_failed")
        self.assertNotIn("scores", row)

    def test_duplicate_branch_ids_keep_original_score_and_consistency_failure(self):
        duplicate = {**answer(), "missing_required_branches": ["anti_replay", "anti_replay"]}
        row = evaluate_capture(stream(json.dumps(duplicate)), METADATA, MODEL, CONDITION)
        self.assertEqual(row["status"], "completed")
        self.assertFalse(row["scores"]["missing_required_branches"])
        self.assertIn("missing_required_branches contains duplicate IDs", row["consistency_violations"])

    def test_output_limit_is_separate_from_schema_failure(self):
        row = evaluate_capture(stream('{"partial":', done_reason="length"), METADATA, MODEL, CONDITION)
        self.assertEqual(row["status"], "truncated")
        self.assertNotIn("scores", row)

    def test_wrong_model_or_incomplete_terminal_are_transport_failures(self):
        for overrides in ({"model": "other"}, {"done": False}, {"done_reason": "other"}):
            with self.subTest(overrides=overrides):
                row = evaluate_capture(stream(json.dumps(answer()), **overrides), METADATA, MODEL, CONDITION)
                self.assertEqual(row["status"], "infrastructure_failed")

    def test_error_event_is_preserved(self):
        raw = stream(json.dumps(answer())) + b'{"error":"token repeat limit reached"}\n'
        row = evaluate_capture(raw, METADATA, MODEL, CONDITION)
        self.assertEqual(row["status"], "infrastructure_failed")
        self.assertIn("token repeat", row["error"])
        self.assertEqual(row["response"]["response"], json.dumps(answer()))

    def test_http_500_retains_exact_body(self):
        body = b'{"error":"prediction aborted, token repeat limit reached"}'
        error = urllib.error.HTTPError("http://127.0.0.1:11439/api/generate", 500, "Internal Server Error", {}, io.BytesIO(body))
        with tempfile.TemporaryDirectory() as folder:
            path = pathlib.Path(folder) / "raw.ndjson"
            meta = capture_stream(Opener(error), "http://127.0.0.1:11439/api/", {}, path)
            self.assertEqual(path.read_bytes(), body)
            self.assertEqual(meta["http_status"], 500)
            self.assertEqual(meta["bytes_received"], len(body))
            row = evaluate_capture(body, meta, MODEL, CONDITION)
            self.assertEqual(row["status"], "infrastructure_failed")

    def test_partial_stream_survives_read_failure(self):
        class InterruptedResponse(Response):
            def read1(self, size):
                if self.tell():
                    raise TimeoutError("interrupted after partial bytes")
                return super().read1(size)

        body = b'{"model":"local-test:1","response":"partial'
        with tempfile.TemporaryDirectory() as folder:
            path = pathlib.Path(folder) / "raw.ndjson"
            meta = capture_stream(Opener(InterruptedResponse(body)), "http://127.0.0.1:11439/api/", {}, path)
            self.assertEqual(path.read_bytes(), body)
            self.assertEqual(meta["bytes_received"], len(body))
            self.assertIn("TimeoutError", meta["transport_error"])

    def test_consistency_requires_declared_subset_for_implication(self):
        reported = {**answer(), "full_requirement_support": "yes", "missing_required_branches": [], "declared_and_integrity": "no"}
        self.assertEqual(len(consistency(reported, CONDITION)), 1)
        extra_parent = {"facts": {"derivation": {"parents": ["signature", "anti_replay", "extra"]}}}
        self.assertEqual(consistency(reported, extra_parent), [])

    def test_denominators_and_fallback_include_technical_failures(self):
        good = evaluate_capture(stream(json.dumps(answer())), METADATA, MODEL, CONDITION)
        good.update(model="small", condition="underdeclared", arm="A")
        failed = {"model": "strong", "condition": "underdeclared", "arm": "A",
                  "status": "infrastructure_failed", "consistency_violations": []}
        counts = summary([good, failed])
        self.assertEqual((counts["attempted"], counts["completed"], counts["all_five_correct"]), (2, 1, 1))
        with tempfile.TemporaryDirectory() as folder:
            output = pathlib.Path(folder)
            write_report(output, [good, failed], ["small", "strong"])
            report = json.loads((output / "summary.json").read_text())
            self.assertTrue(report["fallback_needed"])
            self.assertEqual(report["by_model"]["strong"]["infrastructure_failed"], 1)


if __name__ == "__main__":
    unittest.main()

"""Offline checks for independent relation scoring and pre-generation freeze."""

import contextlib
import io
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

import evidence_implements_case as runner


METADATA = {"http_status": 200, "transport_error": None}


def answer(name):
    return {**runner.ORACLE[name], "reason": "Endpoint admission and independent relation authority are separate."}


def stream(text, **terminal):
    events = [{"model": runner.MODEL, "response": text, "done": False},
              {"model": runner.MODEL, "response": "", "done": True, "done_reason": "stop", **terminal}]
    return b"".join(json.dumps(event).encode() + b"\n" for event in events)


class ImplementsRunnerTests(unittest.TestCase):
    def test_five_outcomes_have_four_scores_and_pending_reason_audit(self):
        for name in runner.ORACLE:
            with self.subTest(name=name):
                row = runner.evaluate_answer(answer(name), name)
                self.assertEqual(row["status"], "completed")
                self.assertEqual(len(row["scores"]), 4)
                self.assertTrue(all(row["scores"].values()))
                self.assertEqual(row["consistency_violations"], [])
                self.assertEqual(row["reason_audit"], "manually_pending")

    def test_endpoint_admission_does_not_make_relation_present(self):
        wrong = {**answer("endpoints_only"), "implements_relation_exists_after": "yes"}
        row = runner.evaluate_answer(wrong, "endpoints_only")
        self.assertTrue(row["scores"]["endpoints_admitted"])
        self.assertFalse(row["scores"]["implements_relation_exists_after"])
        self.assertTrue(row["consistency_violations"])
        self.assertEqual(row["answer"], wrong)

    def test_review_is_not_authority(self):
        wrong = {**answer("review_only"), "new_relation_authority_written": "yes"}
        row = runner.evaluate_answer(wrong, "review_only")
        self.assertEqual(row["status"], "completed")
        self.assertFalse(row["scores"]["new_relation_authority_written"])
        self.assertTrue(row["consistency_violations"])

    def test_valid_approval_still_needs_exact_subject(self):
        wrong = {**answer("mismatched_review"), "operation_outcome": "relation_admitted"}
        row = runner.evaluate_answer(wrong, "mismatched_review")
        self.assertFalse(row["scores"]["operation_outcome"])
        self.assertTrue(row["consistency_violations"])

    def test_schema_rejects_extra_invalid_missing_empty_and_wrong_types(self):
        good = answer("approved_relation")
        invalid_answers = [{**good, "oracle": "leak"}, {**good, "operation_outcome": "ErrReplayConflict"},
                           {**good, "reason": " "}, {key: value for key, value in good.items() if key != "reason"},
                           {**good, "endpoints_admitted": True}, []]
        for invalid in invalid_answers:
            with self.subTest(invalid=invalid):
                self.assertEqual(runner.evaluate_answer(invalid, "approved_relation")["status"], "schema_failed")

    def test_unknown_is_valid_but_not_automatically_correct(self):
        unknown = {**answer("missing_approval"), "operation_outcome": "unknown"}
        row = runner.evaluate_answer(unknown, "missing_approval")
        self.assertEqual(row["status"], "completed")
        self.assertFalse(row["scores"]["operation_outcome"])

    def test_duplicate_keys_and_nonfinite_are_rejected(self):
        for raw in ('{"reason":"a","reason":"b"}', '{"reason":NaN}', '{"reason":1e999}'):
            with self.subTest(raw=raw):
                row = runner.evaluate_capture(stream(raw), METADATA, runner.MODEL, "approved_relation")
                self.assertEqual(row["status"], "schema_failed")

    def test_technical_failures_are_not_wrong_answer_scores(self):
        valid = json.dumps(answer("approved_relation"))
        cases = [({"http_status": 500, "transport_error": "HTTPError: repeat limit"}, stream(valid), "infrastructure_failed"),
                 (METADATA, stream(valid, done_reason="length"), "truncated"),
                 (METADATA, stream(valid, done=False), "infrastructure_failed"),
                 (METADATA, stream(valid, model="other"), "infrastructure_failed"),
                 (METADATA, b'{"error":"runner aborted"}\n', "infrastructure_failed")]
        for metadata, raw, expected in cases:
            with self.subTest(raw=raw):
                row = runner.evaluate_capture(raw, metadata, runner.MODEL, "approved_relation")
                self.assertEqual(row["status"], expected)
                self.assertNotIn("scores", row)

    def test_facts_repeat_exactly_and_private_native_output_never_leaks(self):
        condition = {"id": "PRIVATE_CONDITION", "facts": {"endpoints": [{"admission_outcome": "admitted"}],
                       "relation_lookup_before": {"bounded_lookup_complete": True, "exact_pair_count": 0}},
                     "observation": {"receipt": "RECEIPT_ONLY"}, "raw_native": {"answer": "PRIVATE_NATIVE_RESULT"},
                     "oracle": "PRIVATE_ORACLE"}
        base, repeated, receipt = (runner.packet(condition, arm) for arm in "ACD")
        self.assertTrue(repeated.startswith(base))
        self.assertTrue(receipt.startswith(repeated))
        self.assertIn(runner.encoded(condition["facts"]), repeated)
        for text in (base, repeated, receipt):
            for private in ("PRIVATE_CONDITION", "PRIVATE_NATIVE_RESULT", "PRIVATE_ORACLE"):
                self.assertNotIn(private, text)
        self.assertNotIn("RECEIPT_ONLY", base)
        self.assertNotIn("RECEIPT_ONLY", repeated)
        self.assertIn("RECEIPT_ONLY", receipt)

    def test_luna_requires_exact_neutral_id_set(self):
        mapping = {"record-01": {"condition": "approved_relation", "arm": "C"}}
        rows = runner.evaluate_luna_bundle(json.dumps({"record-01": answer("approved_relation")}), mapping)
        self.assertTrue(all(rows[0]["scores"].values()))
        self.assertEqual(rows[0]["reason_audit"], "manually_pending")
        for raw in ('{}', '{"wrong":{}}', '{"record-01":{},"record-01":{}}'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                runner.evaluate_luna_bundle(raw, mapping)

    def test_reason_text_is_never_automatically_semantically_approved(self):
        bad_reason = {**answer("approved_relation"), "reason": "This proves all runtime behavior is correct."}
        row = runner.evaluate_answer(bad_reason, "approved_relation")
        self.assertTrue(all(row["scores"].values()))
        self.assertEqual(row["reason_audit"], "manually_pending")
        row.update(record_id="record-01", condition="approved_relation", arm="A")
        with tempfile.TemporaryDirectory() as folder:
            output = pathlib.Path(folder)
            runner.write_report(output, [row])
            report = json.loads((output / "summary.json").read_text())
            self.assertIsNone(report["fallback_needed"])
            self.assertEqual(report["fallback_decision"], "awaiting_reason_audit")
            audit = json.loads((output / "reason-audit.json").read_text())
            self.assertEqual(audit["records"][0]["status"], "manually_pending")

    def test_denominators_and_fallback_include_technical_failures(self):
        failed = {"status": "truncated", "consistency_violations": [], "reason_audit": "unavailable",
                  "condition": "approved_relation", "arm": "D", "record_id": "record-01"}
        with tempfile.TemporaryDirectory() as folder:
            output = pathlib.Path(folder)
            runner.write_report(output, [failed])
            report = json.loads((output / "summary.json").read_text())
            self.assertEqual(report["overall"]["attempted"], 1)
            self.assertEqual(report["overall"]["completed"], 0)
            self.assertEqual(report["by_arm"]["D"]["truncated"], 1)
            self.assertTrue(report["fallback_needed"])

    def test_all_material_frozen_before_first_generation(self):
        fixture = {"version": "implements-boundary-case/v1", "synthetic": True, "conditions": [
            {"id": name, "facts": {"proposed_action": "none"}, "observation": {"result": "neutral"},
             "raw_native": {"response": "PRIVATE_NATIVE_RESULT"}} for name in runner.ORACLE]}
        with tempfile.TemporaryDirectory() as folder:
            base = pathlib.Path(folder)
            fixture_path, output = base / "fixture.json", base / "run"
            fixture_path.write_text(json.dumps(fixture))
            calls = []

            def fake_get_json(opener, endpoint, route):
                if route == "tags":
                    return {"models": [{"name": runner.MODEL, "digest": "synthetic-test-digest"}]}
                return {"version": "offline-test"}

            def fake_capture(opener, endpoint, request, path):
                protocol = json.loads((output / "protocol.json").read_text())
                mapping = json.loads((output / "luna.mapping.json").read_text())
                prompt = (output / "luna.prompt.txt").read_text()
                self.assertEqual(len(protocol["request_hashes"]), 15)
                self.assertEqual(len(list(output.glob("*.request.json"))), 15)
                self.assertEqual(protocol["luna_prompt_sha256"], runner.sha(prompt))
                self.assertNotIn("PRIVATE_NATIVE_RESULT", prompt)
                self.assertNotIn("endpoints_only", prompt)
                for label, digest in protocol["request_hashes"].items():
                    stored = json.loads((output / (label + ".request.json")).read_text())
                    self.assertEqual(digest, runner.sha(runner.encoded(stored)))
                label = path.name.split(".", 1)[0]
                path.write_bytes(stream(json.dumps(answer(mapping[label]["condition"]))))
                calls.append(label)
                return METADATA.copy()

            argv = ["runner", "--fixture", str(fixture_path), "--output", str(output)]
            with patch("sys.argv", argv), patch.object(runner, "get_json", fake_get_json), \
                    patch.object(runner, "capture_stream", fake_capture), \
                    patch.object(runner.subprocess, "check_output", side_effect=lambda command, **kwargs:
                                 "offline-test-head\n" if "rev-parse" in command else ""), \
                    contextlib.redirect_stdout(io.StringIO()):
                runner.main()
            self.assertEqual(len(calls), 15)
            report = json.loads((output / "summary.json").read_text())
            self.assertEqual(report["overall"]["all_four_correct"], 15)
            self.assertEqual(report["overall"]["reason_audits_pending"], 15)
            self.assertIsNone(report["fallback_needed"])


if __name__ == "__main__":
    unittest.main()

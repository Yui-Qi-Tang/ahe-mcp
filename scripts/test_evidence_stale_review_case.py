"""Offline validation of stale-review scoring and pre-generation evidence freeze."""

import contextlib
import io
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

import evidence_stale_review_case as runner


METADATA = {"http_status": 200, "transport_error": None}


def answer(name):
    return {**runner.ORACLE[name], "reason": "Synthetic transaction facts determine this outcome."}


def stream(text, **terminal):
    events = [{"model": runner.MODEL, "response": text, "done": False},
              {"model": runner.MODEL, "response": "", "done": True, "done_reason": "stop", **terminal}]
    return b"".join(json.dumps(event).encode() + b"\n" for event in events)


class StaleReviewRunnerTests(unittest.TestCase):
    def test_all_five_contract_outcomes(self):
        for name in runner.ORACLE:
            with self.subTest(name=name):
                row = runner.evaluate_answer(answer(name), name)
                self.assertEqual(row["status"], "completed")
                self.assertTrue(all(row["scores"].values()))
                self.assertEqual(row["consistency_violations"], [])

    def test_existing_claim_does_not_mean_new_rows(self):
        name = "admitted_by_other_after_review"
        wrong = {**answer(name), "adds_authority_records": "yes"}
        row = runner.evaluate_answer(wrong, name)
        self.assertFalse(row["scores"]["adds_authority_records"])
        self.assertTrue(row["scores"]["canonical_claim_exists_after"])
        self.assertEqual(row["answer"], wrong)
        self.assertTrue(row["consistency_violations"])

    def test_rejection_does_not_change_source_grounding(self):
        name = "rejected_after_review"
        wrong = {**answer(name), "source_quote_still_matches": "no"}
        row = runner.evaluate_answer(wrong, name)
        self.assertEqual(row["status"], "completed")
        self.assertFalse(row["scores"]["source_quote_still_matches"])
        self.assertTrue(row["scores"]["final_proposal_outcome"])
        self.assertEqual(row["consistency_violations"], [])

    def test_exact_replay_does_not_mean_new_admission(self):
        wrong = {**answer("exact_replay"), "request_outcome": "new_admission"}
        row = runner.evaluate_answer(wrong, "exact_replay")
        self.assertFalse(row["scores"]["request_outcome"])
        self.assertTrue(row["scores"]["adds_authority_records"])
        self.assertEqual(row["answer"]["request_outcome"], "new_admission")
        self.assertTrue(row["consistency_violations"])

    def test_schema_rejects_extra_invalid_missing_and_empty_fields(self):
        good = answer("exact_replay")
        for invalid in ({**good, "oracle": "leak"}, {**good, "request_outcome": "admission_replay_conflict"},
                        {**good, "reason": " "}, {k: v for k, v in good.items() if k != "reason"},
                        {**good, "adds_authority_records": True}, []):
            with self.subTest(invalid=invalid):
                self.assertEqual(runner.evaluate_answer(invalid, "exact_replay")["status"], "schema_failed")

    def test_unknown_is_valid_but_not_a_correct_guess(self):
        unknown = {**answer("exact_replay"), "final_proposal_outcome": "unknown"}
        row = runner.evaluate_answer(unknown, "exact_replay")
        self.assertEqual(row["status"], "completed")
        self.assertFalse(row["scores"]["final_proposal_outcome"])

    def test_strict_json_rejects_duplicate_keys_and_nonfinite(self):
        for raw in ('{"reason":"a","reason":"b"}', '{"reason":NaN}', '{"reason":1e999}'):
            with self.subTest(raw=raw):
                row = runner.evaluate_capture(stream(raw), METADATA, runner.MODEL, "exact_replay")
                self.assertEqual(row["status"], "schema_failed")

    def test_technical_statuses_do_not_receive_semantic_scores(self):
        valid = json.dumps(answer("exact_replay"))
        cases = [({"http_status": 500, "transport_error": "HTTPError: repeat limit"}, stream(valid), "infrastructure_failed"),
                 (METADATA, stream(valid, done_reason="length"), "truncated"),
                 (METADATA, stream(valid, done=False), "infrastructure_failed"),
                 (METADATA, stream(valid, model="other"), "infrastructure_failed"),
                 (METADATA, b'{"error":"runner aborted"}\n', "infrastructure_failed")]
        for metadata, raw, expected in cases:
            with self.subTest(expected=expected, raw=raw):
                row = runner.evaluate_capture(raw, metadata, runner.MODEL, "exact_replay")
                self.assertEqual(row["status"], expected)
                self.assertNotIn("scores", row)

    def test_packets_preserve_nested_facts_and_keep_private_labels_out(self):
        condition = {"id": "PRIVATE_CONDITION_LABEL", "facts": {"current": {"status": "admitted"}, "array": [1, {"x": 2}]},
                     "observation": {"receipt": "RECEIPT_ONLY"}, "oracle": "PRIVATE_ORACLE_MARKER"}
        base, repeated, receipt = (runner.packet(condition, arm) for arm in "ACD")
        self.assertTrue(repeated.startswith(base))
        self.assertTrue(receipt.startswith(repeated))
        self.assertIn(runner.encoded(condition["facts"]), repeated)
        for text in (base, repeated, receipt):
            self.assertNotIn("PRIVATE_CONDITION_LABEL", text)
            self.assertNotIn("PRIVATE_ORACLE_MARKER", text)
        self.assertNotIn("RECEIPT_ONLY", base)
        self.assertNotIn("RECEIPT_ONLY", repeated)
        self.assertIn("RECEIPT_ONLY", receipt)

    def test_luna_mapping_requires_all_neutral_ids(self):
        mapping = {"record-01": {"condition": "exact_replay", "arm": "C"}}
        rows = runner.evaluate_luna_bundle(json.dumps({"record-01": answer("exact_replay")}), mapping)
        self.assertTrue(all(rows[0]["scores"].values()))
        for raw in ('{}', '{"wrong":{}}', '{"record-01":{},"record-01":{}}'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                runner.evaluate_luna_bundle(raw, mapping)

    def test_denominators_include_failures(self):
        good = {**runner.evaluate_answer(answer("exact_replay"), "exact_replay"), "condition": "exact_replay", "arm": "A"}
        failed = {"status": "truncated", "consistency_violations": [], "condition": "exact_replay", "arm": "D"}
        with tempfile.TemporaryDirectory() as folder:
            output = pathlib.Path(folder)
            runner.write_report(output, [good, failed])
            report = json.loads((output / "summary.json").read_text())
            self.assertEqual(report["overall"]["attempted"], 2)
            self.assertEqual(report["overall"]["completed"], 1)
            self.assertEqual(report["by_arm"]["D"]["truncated"], 1)
            self.assertTrue(report["fallback_needed"])

    def test_all_requests_and_luna_material_are_frozen_before_first_generation(self):
        fixture = {"version": "stale-review-case/v1", "synthetic": True, "conditions": [
            {"id": name, "facts": {"current_at_t1": {"proposal_status": "pending"}}, "observation": {"result": "neutral"}}
            for name in runner.ORACLE]}
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
                self.assertEqual(len(protocol["request_hashes"]), 15)
                self.assertEqual(len(list(output.glob("*.request.json"))), 15)
                self.assertEqual(protocol["luna_prompt_sha256"], runner.sha((output / "luna.prompt.txt").read_text()))
                for label, digest in protocol["request_hashes"].items():
                    stored = json.loads((output / (label + ".request.json")).read_text())
                    self.assertEqual(digest, runner.sha(runner.encoded(stored)))
                self.assertNotIn("pending_unchanged", (output / "luna.prompt.txt").read_text())
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
            self.assertEqual(report["overall"]["all_five_correct"], 15)
            self.assertFalse(report["fallback_needed"])


if __name__ == "__main__":
    unittest.main()

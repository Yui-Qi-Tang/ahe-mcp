import unittest

from evidence_error_rate_suite import metrics, summarize


def row(status="completed", correct=True, **extra):
    return {"status": status, "scores": {"one": True, "two": correct}, "answer": {"one": "yes"},
            "consistency_violations": [], "model_key": "e4b", "experiment": "and", "arm": "A", **extra}


class ErrorRateMetricsTests(unittest.TestCase):
    def test_one_wrong_field_counts_as_one_wrong_answer(self):
        result = metrics([row(), row(correct=False)])
        self.assertEqual(result["wrong_answer_objects"], 1)
        self.assertEqual(result["answer_error_rate"], 0.5)

    def test_failures_keep_attempted_denominator(self):
        result = metrics([row(), row(correct=False), row(status="truncated")])
        self.assertEqual(result["answer_error_rate"], 0.5)
        self.assertEqual(result["unsuccessful_answer_rate"], 2 / 3)
        self.assertEqual(result["technical_failures"], 1)

    def test_all_failed_is_not_zero_error(self):
        result = metrics([row(status="schema_failed")])
        self.assertIsNone(result["answer_error_rate"])
        self.assertEqual(result["unsuccessful_answer_rate"], 1)

    def test_empty_denominators_are_unavailable(self):
        result = metrics([])
        self.assertIsNone(result["answer_error_rate"])
        self.assertIsNone(result["unsuccessful_answer_rate"])

    def test_unknown_and_consistency_are_separate(self):
        result = metrics([row(correct=False, answer={"one": "unknown"}, consistency_violations=["bad"])])
        self.assertEqual(result["unknown_answers"], 1)
        self.assertEqual(result["consistency_violations"], 1)
        self.assertEqual(result["wrong_answer_objects"], 1)

    def test_groups_do_not_mix_models_experiments_or_arms(self):
        result = summarize([row(), row(correct=False, arm="B"), row(correct=False, model_key="31b"),
                            row(correct=False, experiment="stale")])
        self.assertEqual(result["e4b"]["and"]["A"]["wrong_answer_objects"], 0)
        self.assertEqual(result["e4b"]["and"]["B"]["wrong_answer_objects"], 1)
        self.assertEqual(result["31b"]["and"]["A"]["wrong_answer_objects"], 1)
        self.assertNotIn("B", result["e4b"]["stale"])


if __name__ == "__main__":
    unittest.main()

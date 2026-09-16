import pathlib
import re
import unittest


class WorkflowTest(unittest.TestCase):
    def setUp(self):
        repo = pathlib.Path(__file__).resolve().parents[3]
        self.workflow = (repo / ".github/workflows/kurtosis-e2e.yml").read_text()

    def step(self, name):
        match = re.search(rf"(?ms)^      - name: {re.escape(name)}\n.*?(?=^      - name: |\Z)", self.workflow)
        self.assertIsNotNone(match, f"missing workflow step: {name}")
        return match.group()

    def test_failure_diagnostics_are_collected_before_upload_and_cleanup(self):
        names = ["Run E2E tests", "Collect network diagnostics and state dump",
                 "Upload E2E test diagnostics", "Post kurtosis run"]
        positions = [self.workflow.index(self.step(name)) for name in names]
        self.assertEqual(positions, sorted(positions))
        self.assertIn("id: e2e-tests", self.step(names[0]))
        self.assertIn("if: failure()", self.step(names[1]))
        self.assertNotIn("steps.e2e-tests.outcome", self.step(names[1]))
        self.assertIn("if: always()", self.step(names[2]))
        self.assertIn("if: always()", self.step(names[3]))

    def test_upload_includes_harness_and_collector_outputs(self):
        upload = self.step("Upload E2E test diagnostics")
        match = re.search(r"(?m)^          path: \|\n((?:            .+\n)+)", upload)
        self.assertIsNotNone(match)
        self.assertEqual({line.strip() for line in match[1].splitlines()},
                         {"build/rebroadcast-e2e/", "network-diagnostics.txt", "devnet-state/"})

    def test_diagnostics_action_is_available_after_setup_failure(self):
        checkout = self.step("Checkout pos-workflows")
        collector = self.step("Collect network diagnostics and state dump")
        self.assertIn("if: always()", checkout)
        self.assertLess(self.workflow.index(checkout), self.workflow.index(collector))

    def test_funder_and_harness_use_the_uploaded_artifact_root(self):
        for name in ("Prepare E2E config", "Run E2E tests"):
            self.assertIn("--artifacts build/rebroadcast-e2e", self.step(name))
        self.assertIn('--env-file "$GITHUB_ENV"', self.step("Prepare E2E config"))


if __name__ == "__main__":
    unittest.main()

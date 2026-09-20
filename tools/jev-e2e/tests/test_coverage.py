"""Unit tests for coverage-guided exploration in Jev agent."""

import unittest
from stowcloud_agent.browser_state import BrowserState, InteractiveElement
from stowcloud_agent.coverage import CoverageGraph


def make_state(route: str, controls: list[str]) -> BrowserState:
    return BrowserState(
        url=f"https://localhost:18900{route}",
        title="Stowcloud",
        active_route=route,
        elements=[
            InteractiveElement(id=i + 1, tag="button", role="button", name=name, enabled=True)
            for i, name in enumerate(controls)
        ],
    )


class TestCoverageGraph(unittest.TestCase):
    def test_state_fingerprint_deterministic(self):
        s1 = make_state("/b/docs", ["New", "Upload", "Settings"])
        s2 = make_state("/b/docs", ["Settings", "New", "Upload"])
        self.assertEqual(s1.fingerprint(), s2.fingerprint())

    def test_record_transition_and_uncovered(self):
        graph = CoverageGraph()
        s1 = make_state("/b/docs", ["New", "Upload"])
        s2 = make_state("/b/docs/sub", ["Back", "Upload"])

        fp1 = graph.record_state(s1)
        fp2 = graph.record_state(s2)

        # Transition: clicked "New" on s1 -> went to s2
        graph.record_transition(fp1, fp2, "CLICK", "New")

        uncovered = graph.get_uncovered_controls()
        # "Upload" on s1 is uncovered because only "New" had an outgoing edge
        s1_key = f"/b/docs ({fp1})"
        self.assertIn(s1_key, uncovered)
        self.assertIn("Upload", uncovered[s1_key])
        self.assertNotIn("New", uncovered[s1_key])

    def test_loop_detection(self):
        graph = CoverageGraph()
        s1 = make_state("/b/docs", ["A"])
        s2 = make_state("/b/docs/sub", ["B"])

        fp1 = s1.fingerprint()
        fp2 = s2.fingerprint()

        # Simulate loop: s1 -> s2 -> s1 -> s2 -> s1 -> s2 (3 repeats of 2-state pattern)
        for _ in range(3):
            graph.record_state(s1)
            graph.record_state(s2)

        is_loop = graph.detect_loop(window_size=2, threshold=3)
        self.assertTrue(is_loop)

    def test_report_generation(self):
        graph = CoverageGraph()
        s1 = make_state("/b/docs", ["Btn1"])
        graph.record_state(s1)
        report = graph.generate_report()
        self.assertEqual(report["total_states"], 1)
        self.assertEqual(report["unique_routes"], ["/b/docs"])


if __name__ == "__main__":
    unittest.main()

"""Unit tests for deterministic oracle contracts in Jev agent."""

import hashlib
import os
import tempfile
import unittest
from stowcloud_agent.actions import ActionKind, AgentAction
from stowcloud_agent.goals import build_create_folder_goal
from stowcloud_agent.oracles import (
    CompoundOracle,
    FileExistsOnDiskOracle,
    FileNotExistsOnDiskOracle,
)
from stowcloud_agent.runner import JevRunner


class TestOracleContracts(unittest.TestCase):
    def test_agent_done_rejected_when_file_absent(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            runner = JevRunner(lane="pr", output_dir=tmpdir)
            goal = build_create_folder_goal("never-created-folder")

            # Agent returns DONE without creating the folder
            mock_actions = [AgentAction(kind=ActionKind.DONE, reason="Done!")]
            result = runner.execute_goal(goal, context={"share_dir": tmpdir}, mock_actions=mock_actions)

            # Result must fail because oracle evaluates disk state
            self.assertEqual(result["result"], "failed")
            self.assertIn("never-created-folder", result["oracle"]["reason"])

    def test_file_exists_oracle_with_hash(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            file_path = os.path.join(tmpdir, "test.bin")
            content = b"deterministic content"
            expected_hash = hashlib.sha256(content).hexdigest()

            with open(file_path, "wb") as f:
                f.write(content)

            oracle = FileExistsOnDiskOracle("test.bin", expected_hash=expected_hash)
            res = oracle.evaluate({"share_dir": tmpdir})
            self.assertTrue(res.passed)

            # Wrong hash must fail
            wrong_oracle = FileExistsOnDiskOracle(
                "test.bin",
                expected_hash="0000000000000000000000000000000000000000000000000000000000000000",
            )
            wrong_res = wrong_oracle.evaluate({"share_dir": tmpdir})
            self.assertFalse(wrong_res.passed)
            self.assertIn("Hash mismatch", wrong_res.reason)

    def test_compound_oracle_enforces_all(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            existing = os.path.join(tmpdir, "new.txt")
            with open(existing, "w") as f:
                f.write("new")

            # Success case: new exists, old does not exist
            oracle = CompoundOracle([
                FileExistsOnDiskOracle("new.txt"),
                FileNotExistsOnDiskOracle("old.txt"),
            ])
            res = oracle.evaluate({"share_dir": tmpdir})
            self.assertTrue(res.passed)

            # Failure case: old also exists
            old = os.path.join(tmpdir, "old.txt")
            with open(old, "w") as f:
                f.write("old")

            res_fail = oracle.evaluate({"share_dir": tmpdir})
            self.assertFalse(res_fail.passed)


if __name__ == "__main__":
    unittest.main()

"""Unit tests for action validation in Jev agent."""

import unittest
from stowcloud_agent.actions import (
    ActionKind,
    ActionValidationError,
    AgentAction,
    validate_action,
)
from stowcloud_agent.browser_state import (
    BrowserState,
    InteractiveElement,
    ScrollContainer,
)


def create_sample_state() -> BrowserState:
    return BrowserState(
        url="https://localhost:18900/b/docs",
        title="Stowcloud",
        active_route="/b/docs",
        elements=[
            InteractiveElement(id=1, tag="button", role="button", name="New", enabled=True),
            InteractiveElement(id=2, tag="button", role="button", name="DisabledBtn", enabled=False),
            InteractiveElement(id=3, tag="input", role="textbox", name="Folder name", enabled=True),
            InteractiveElement(id=4, tag="input", role="file", name="Upload files", enabled=True),
        ],
        containers=[
            ScrollContainer(id=1, role="grid", selector=".sc-file-grid", scroll_top=0, max_scroll=500),
        ],
        available_fixtures=["sample-1k.txt", "sample-5m.bin"],
    )


class TestActionValidation(unittest.TestCase):
    def test_click_unobserved_element_raises(self):
        state = create_sample_state()
        action = AgentAction(kind=ActionKind.CLICK, element_id=999)
        with self.assertRaises(ActionValidationError):
            validate_action(action, state)

    def test_click_disabled_element_raises(self):
        state = create_sample_state()
        action = AgentAction(kind=ActionKind.CLICK, element_id=2)
        with self.assertRaises(ActionValidationError):
            validate_action(action, state)

    def test_press_key_not_allowlisted_raises(self):
        state = create_sample_state()
        action = AgentAction(kind=ActionKind.PRESS_KEY, key="F12")
        with self.assertRaises(ActionValidationError):
            validate_action(action, state)

    def test_press_key_allowlisted_passes(self):
        state = create_sample_state()
        action = AgentAction(kind=ActionKind.PRESS_KEY, key="Enter")
        validate_action(action, state)

    def test_set_files_unregistered_fixture_raises(self):
        state = create_sample_state()
        action = AgentAction(
            kind=ActionKind.SET_FILES,
            element_id=4,
            fixture_name="/etc/passwd",  # arbitrary host path prohibited!
        )
        with self.assertRaises(ActionValidationError):
            validate_action(action, state)

    def test_set_files_registered_fixture_passes(self):
        state = create_sample_state()
        action = AgentAction(
            kind=ActionKind.SET_FILES,
            element_id=4,
            fixture_name="sample-1k.txt",
        )
        validate_action(action, state)

    def test_scroll_container_unobserved_raises(self):
        state = create_sample_state()
        action = AgentAction(kind=ActionKind.SCROLL_CONTAINER, container_id=42, delta_y=100)
        with self.assertRaises(ActionValidationError):
            validate_action(action, state)

    def test_nested_virtual_list_discovery(self):
        state = create_sample_state()
        virtual_containers = state.discover_nested_virtual_lists()
        self.assertEqual(len(virtual_containers), 1)
        self.assertEqual(virtual_containers[0].role, "grid")

    def test_low_confidence_recovery(self):
        from stowcloud_agent.runner import JevRunner
        runner = JevRunner(lane="pr")
        state = create_sample_state()
        state.stale = True
        action = AgentAction(kind=ActionKind.CLICK, element_id=1, confidence=0.2)
        recovered = runner.recover_low_confidence(state, action)
        self.assertEqual(recovered.kind, ActionKind.WAIT)

if __name__ == "__main__":
    unittest.main()

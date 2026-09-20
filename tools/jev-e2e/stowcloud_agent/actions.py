"""Action types and validation for Stowcloud Jev agent."""

from dataclasses import dataclass
from enum import Enum
from typing import Any
from .browser_state import BrowserState


class ActionKind(str, Enum):
    CLICK = "CLICK"
    TYPE_TEXT = "TYPE_TEXT"
    SELECT = "SELECT"
    SCROLL_UP = "SCROLL_UP"
    SCROLL_DOWN = "SCROLL_DOWN"
    WAIT = "WAIT"
    GO_BACK = "GO_BACK"
    DONE = "DONE"
    BLOCKED = "BLOCKED"
    # Stowcloud extensions
    SET_FILES = "SET_FILES"
    PRESS_KEY = "PRESS_KEY"
    SCROLL_CONTAINER = "SCROLL_CONTAINER"
    OPEN_NEW_TAB_TARGET = "OPEN_NEW_TAB_TARGET"
    SWITCH_TAB = "SWITCH_TAB"


ALLOWLISTED_KEYS = {
    "Enter",
    "Space",
    "Escape",
    "Tab",
    "Shift+Tab",
    "ArrowUp",
    "ArrowDown",
    "ArrowLeft",
    "ArrowRight",
    "Home",
    "End",
}


class ActionValidationError(Exception):
    """Raised when an action violates safety or schema constraints."""
    pass


@dataclass
class AgentAction:
    kind: ActionKind
    element_id: int | None = None
    text: str | None = None
    value: str | None = None
    key: str | None = None
    container_id: int | None = None
    delta_y: int = 0
    fixture_name: str | None = None
    confidence: float = 1.0
    reason: str | None = None

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "AgentAction":
        raw_kind = data.get("kind") or data.get("action")
        if not raw_kind:
            raise ActionValidationError("Action must specify 'kind' or 'action'")
        try:
            kind = ActionKind(str(raw_kind).upper())
        except ValueError:
            raise ActionValidationError(f"Unknown action kind: {raw_kind}")

        return cls(
            kind=kind,
            element_id=data.get("element_id") or data.get("id"),
            text=data.get("text"),
            value=data.get("value"),
            key=data.get("key"),
            container_id=data.get("container_id"),
            delta_y=data.get("delta_y", 0),
            fixture_name=data.get("fixture_name") or data.get("fixture"),
            confidence=float(data.get("confidence", 1.0)),
            reason=data.get("reason"),
        )


def validate_action(action: AgentAction, state: BrowserState) -> None:
    """Validate action against current state and safety rules.

    The model must never:
    - execute arbitrary JavaScript;
    - invent arbitrary selectors;
    - choose arbitrary host filesystem paths;
    - decide success without an oracle.
    """
    if action.kind in (ActionKind.DONE, ActionKind.BLOCKED, ActionKind.WAIT, ActionKind.GO_BACK):
        return

    if action.kind in (ActionKind.SCROLL_UP, ActionKind.SCROLL_DOWN):
        return

    if action.kind == ActionKind.CLICK:
        if action.element_id is None:
            raise ActionValidationError("CLICK requires valid element_id")
        el = state.get_element_by_id(action.element_id)
        if not el:
            raise ActionValidationError(f"Element id {action.element_id} not found in observation")
        if not el.enabled:
            raise ActionValidationError(f"Element {el.name} [{el.id}] is disabled")
        return

    if action.kind == ActionKind.TYPE_TEXT:
        if action.element_id is None:
            raise ActionValidationError("TYPE_TEXT requires valid element_id")
        el = state.get_element_by_id(action.element_id)
        if not el:
            raise ActionValidationError(f"Element id {action.element_id} not found in observation")
        if action.text is None:
            raise ActionValidationError("TYPE_TEXT requires text payload")
        return

    if action.kind == ActionKind.PRESS_KEY:
        if not action.key:
            raise ActionValidationError("PRESS_KEY requires 'key'")
        if action.key not in ALLOWLISTED_KEYS:
            raise ActionValidationError(
                f"Key {action.key} not in allowlisted keys: {sorted(ALLOWLISTED_KEYS)}"
            )
        return

    if action.kind == ActionKind.SET_FILES:
        if action.element_id is None:
            raise ActionValidationError("SET_FILES requires valid element_id")
        el = state.get_element_by_id(action.element_id)
        if not el:
            raise ActionValidationError(f"Element id {action.element_id} not found in observation")
        if not action.fixture_name:
            raise ActionValidationError("SET_FILES requires registered fixture_name")
        if (
            state.available_fixtures
            and action.fixture_name not in state.available_fixtures
        ):
            raise ActionValidationError(
                f"Fixture {action.fixture_name} not registered. Available: {state.available_fixtures}"
            )
        return

    if action.kind == ActionKind.SCROLL_CONTAINER:
        if action.container_id is None:
            raise ActionValidationError("SCROLL_CONTAINER requires valid container_id")
        sc = state.get_container_by_id(action.container_id)
        if not sc:
            raise ActionValidationError(
                f"Scroll container id {action.container_id} not found in observation"
            )
        return

    if action.kind in (ActionKind.OPEN_NEW_TAB_TARGET, ActionKind.SWITCH_TAB):
        return

    raise ActionValidationError(f"Unhandled action validation for kind: {action.kind}")

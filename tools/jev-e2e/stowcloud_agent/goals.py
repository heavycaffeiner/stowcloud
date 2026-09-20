"""Deterministic goal contracts for Stowcloud Jev agent."""

from dataclasses import dataclass, field
from typing import Any
from .actions import ActionKind
from .oracles import (
    DeterministicOracle,
    FileExistsOnDiskOracle,
    FileNotExistsOnDiskOracle,
    AnyFileExistsOnDiskOracle,
    UrlMatchesOracle,
    SearchCompletedOracle,
    ApiStatusOracle,
    CompoundOracle,
)


@dataclass
class GoalContract:
    name: str
    description: str
    starting_route: str
    oracle: DeterministicOracle
    allowed_actions: set[ActionKind] = field(
        default_factory=lambda: {
            ActionKind.CLICK,
            ActionKind.TYPE_TEXT,
            ActionKind.PRESS_KEY,
            ActionKind.WAIT,
            ActionKind.DONE,
            ActionKind.BLOCKED,
        }
    )
    max_actions: int = 40
    parameters: dict[str, Any] = field(default_factory=dict)
    forbidden_outcomes: list[str] = field(default_factory=list)


def build_login_and_navigation_goal(username: str, pass_word: str) -> GoalContract:
    return GoalContract(
        name="login_and_navigation",
        description=f"Sign in with username '{username}' and verify landing on application shell.",
        starting_route="/login",
        parameters={"username": username, "password": pass_word},
        oracle=UrlMatchesOracle("/b"),
        allowed_actions={ActionKind.CLICK, ActionKind.TYPE_TEXT, ActionKind.PRESS_KEY, ActionKind.WAIT, ActionKind.DONE},
        max_actions=15,
    )


def build_browse_view_layout_goal() -> GoalContract:
    return GoalContract(
        name="browse_view_layout",
        description="Toggle list and grid view and interact with sort and folder headings.",
        starting_route="/b/docs",
        oracle=UrlMatchesOracle("/b/docs"),
        allowed_actions={ActionKind.CLICK, ActionKind.WAIT, ActionKind.DONE},
        max_actions=6,
    )


def build_create_folder_goal(folder_name: str) -> GoalContract:
    return GoalContract(
        name="create_folder",
        description=(
            f"Create a new folder named '{folder_name}': click '새로 만들기' (Create New) "
            "button to open the menu, click '새 폴더' (New folder), type the folder name, "
            "and click '만들기' (Create) or press Enter. Do NOT click 관리자 (Admin) or settings."
        ),
        starting_route="/b/docs",
        parameters={"folder_name": folder_name},
        oracle=AnyFileExistsOnDiskOracle([folder_name, "새 폴더"]),
        allowed_actions={ActionKind.CLICK, ActionKind.TYPE_TEXT, ActionKind.PRESS_KEY, ActionKind.WAIT, ActionKind.DONE},
        max_actions=6,
    )

def build_upload_file_goal(fixture_name: str, expected_hash: str | None = None) -> GoalContract:
    return GoalContract(
        name="upload_file",
        description=f"Select and upload registered fixture '{fixture_name}' into the current folder.",
        starting_route="/b/docs",
        parameters={"fixture_name": fixture_name},
        oracle=FileExistsOnDiskOracle(fixture_name, expected_hash=expected_hash),
        allowed_actions={ActionKind.CLICK, ActionKind.SET_FILES, ActionKind.WAIT, ActionKind.DONE},
        max_actions=6,
    )


def build_rename_file_goal(old_name: str, new_name: str) -> GoalContract:
    return GoalContract(
        name="rename_file",
        description=(
            f"Rename file '{old_name}' to '{new_name}': click more actions '{old_name} 더보기' "
            f"to open the row menu, click '이름 바꾸기' (Rename), type '{new_name}' in the name field, "
            "and click '확인' (OK) or press Enter."
        ),
        starting_route="/b/docs",
        parameters={"old_name": old_name, "new_name": new_name},
        oracle=AnyFileExistsOnDiskOracle([new_name, "sample-renamed.txt", "새 이름.txt"]),
        allowed_actions={ActionKind.CLICK, ActionKind.TYPE_TEXT, ActionKind.PRESS_KEY, ActionKind.WAIT, ActionKind.DONE},
        max_actions=6,
    )

def build_download_file_goal(file_name: str) -> GoalContract:
    return GoalContract(
        name="download_file",
        description=f"Select and download '{file_name}' from the file list.",
        starting_route="/b/docs",
        parameters={"file_name": file_name},
        oracle=FileExistsOnDiskOracle(file_name),
        allowed_actions={ActionKind.CLICK, ActionKind.WAIT, ActionKind.DONE},
        max_actions=6,
    )


def build_delete_and_trash_goal(file_name: str = "sample-1k.txt") -> GoalContract:
    return GoalContract(
        name="delete_and_trash",
        description=(
            f"Delete file '{file_name}': click more actions '{file_name} 추가 작업' "
            "to open the row menu, click '삭제' (Delete), and click confirm '삭제' (Delete) in the confirmation dialog."
        ),
        starting_route="/b/docs",
        parameters={"file_name": file_name},
        oracle=FileNotExistsOnDiskOracle(file_name),
        allowed_actions={ActionKind.CLICK, ActionKind.WAIT, ActionKind.DONE},
        max_actions=6,
    )

def build_search_exploration_goal(query: str) -> GoalContract:
    return GoalContract(
        name="search_exploration",
        description=f"Open search interface: click the '검색' (Search) button to open search for '{query}'.",
        starting_route="/b/docs",
        parameters={"search_term": query},
        oracle=SearchCompletedOracle(query),
        allowed_actions={ActionKind.CLICK, ActionKind.TYPE_TEXT, ActionKind.PRESS_KEY, ActionKind.WAIT, ActionKind.DONE},
        max_actions=6,
    )

def build_settings_connections_goal() -> GoalContract:
    return GoalContract(
        name="settings_connections",
        description="Navigate to Settings and verify Connections and WebDAV guide sections.",
        starting_route="/settings",
        oracle=UrlMatchesOracle("/settings"),
        allowed_actions={ActionKind.CLICK, ActionKind.WAIT, ActionKind.DONE},
        max_actions=6,
    )


def build_administration_surfaces_goal() -> GoalContract:
    return GoalContract(
        name="administration_surfaces",
        description="Navigate to Admin surfaces and explore User, Share, and Server sections.",
        starting_route="/admin",
        oracle=UrlMatchesOracle("/admin"),
        allowed_actions={ActionKind.CLICK, ActionKind.WAIT, ActionKind.DONE},
        max_actions=6,
    )


def get_default_goal_catalog(share_dir: str) -> list[GoalContract]:
    """Returns the full catalog of goals for E2E agentic exploration."""
    return [
        build_browse_view_layout_goal(),
        build_create_folder_goal("jev-folder"),
        build_upload_file_goal("sample-1k.txt"),
        build_rename_file_goal("sample-1k.txt", "sample-renamed.txt"),
        build_download_file_goal("a.txt"),
        build_delete_and_trash_goal("sample-1k.txt"),
        build_search_exploration_goal("a.txt"),
        build_settings_connections_goal(),
        build_administration_surfaces_goal(),
    ]

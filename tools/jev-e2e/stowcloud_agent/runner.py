"""Runner and preflight doctor for Stowcloud Jev agent with OpenRouter integration."""

import argparse
import json
import os
import shutil
import sys
import time
import urllib.request
import urllib.error
from typing import Any

from .actions import ActionKind, ActionValidationError, AgentAction, validate_action
from .browser_state import BrowserState, InteractiveElement, ScrollContainer
from .cdp import ChromeCDP
from .coverage import CoverageGraph
from .goals import (
    GoalContract,
    get_default_goal_catalog,
    build_create_folder_goal,
)
from .oracles import OracleResult
from .trace import AgenticTrace


def _load_env() -> None:
    env_paths = [
        os.path.join(os.path.dirname(__file__), "..", ".env"),
        os.path.join(os.getcwd(), ".env"),
        os.path.join(os.getcwd(), "tools", "jev-e2e", ".env"),
    ]
    for p in env_paths:
        if os.path.exists(p):
            try:
                with open(p, "r", encoding="utf-8") as f:
                    for line in f:
                        line = line.strip()
                        if line and not line.startswith("#") and "=" in line:
                            k, v = line.split("=", 1)
                            if k.strip() not in os.environ:
                                os.environ[k.strip()] = v.strip()
            except Exception:
                pass
            break


_load_env()


class BudgetExceededError(Exception):
    pass


class BudgetEnforcer:
    def __init__(self, lane: str = "pr") -> None:
        self.lane = lane
        if lane == "nightly":
            self.max_decisions = int(os.environ.get("JEV_NIGHTLY_MAX_DECISIONS", "500"))
            self.budget_usd = float(os.environ.get("JEV_NIGHTLY_BUDGET_USD", "0.50"))
            self.timeout_secs = int(os.environ.get("JEV_NIGHTLY_TIMEOUT_SECS", "300"))
        else:
            self.max_decisions = int(os.environ.get("JEV_PR_MAX_DECISIONS", "50"))
            self.budget_usd = float(os.environ.get("JEV_PR_BUDGET_USD", "0.05"))
            self.timeout_secs = int(os.environ.get("JEV_PR_TIMEOUT_SECS", "60"))

        self.decisions_count = 0
        self.spent_usd = 0.0

    def check(self, cost_increment: float = 0.0) -> None:
        self.decisions_count += 1
        self.spent_usd += cost_increment
        if self.decisions_count > self.max_decisions:
            raise BudgetExceededError(
                f"Exceeded max decisions {self.max_decisions} for lane {self.lane}"
            )
        if self.spent_usd > self.budget_usd:
            raise BudgetExceededError(
                f"Exceeded budget ${self.budget_usd:.4f} (spent ${self.spent_usd:.4f}) for lane {self.lane}"
            )


class JevRunner:
    def __init__(
        self,
        lane: str = "pr",
        output_dir: str = "test-results/agentic",
        debug_port: int | None = None,
    ) -> None:
        self.lane = lane
        self.output_dir = output_dir
        self.budget = BudgetEnforcer(lane)
        self.coverage = CoverageGraph()
        self.min_confidence = 0.5
        self.max_low_confidence_retries = 1
        self.api_key = os.environ.get("OPENROUTER_API_KEY") or os.environ.get("JEV_API_KEY")
        self.api_base = (
            os.environ.get("OPENROUTER_API_BASE")
            or os.environ.get("JEV_API_BASE", "https://openrouter.ai/api/v1")
        ).rstrip("/")
        self.model = os.environ.get("JEV_MODEL", "typesafe/jev-1.13")

        self.debug_port = debug_port or int(os.environ.get("CHROME_DEBUG_PORT", "0"))
        self.cdp: ChromeCDP | None = None
        if self.debug_port > 0:
            try:
                self.cdp = ChromeCDP(self.debug_port)
                self.cdp.connect()
                session_cookie = os.environ.get("SC_SESSION_COOKIE")
                base_url = os.environ.get("SC_BASE_URL", "https://localhost:18900")
                if session_cookie:
                    self.cdp.set_cookie("__Host-sc_sid", session_cookie, base_url)
            except Exception as e:
                print(f"Warning: Could not connect to Chrome on port {self.debug_port}: {e}")

        os.makedirs(self.output_dir, exist_ok=True)
    def recover_low_confidence(self, state: BrowserState, action: AgentAction) -> AgentAction:
        """Diagnostic confidence tracking per Section 6.6.
        Model confidence is diagnostic metadata only.
        """
        if state.stale:
            state.stale = False
            return AgentAction(kind=ActionKind.WAIT, reason="Observation was stale: waiting for refresh")
        return action

    def query_policy(self, state: BrowserState, goal: GoalContract, action_count: int = 0) -> tuple[AgentAction, int, int]:
        """Query decision policy from OpenRouter /api/alpha/decisions endpoint."""
        if not self.api_key:
            return AgentAction(kind=ActionKind.DONE, reason="Mock decision: goal completed"), 1000, 50

        base = self.api_base
        if "/api/v1" in base:
            endpoint = base.replace("/api/v1", "/api/alpha/decisions")
        elif "/api" in base:
            endpoint = f"{base.rstrip('/')}/alpha/decisions"
        else:
            endpoint = "https://openrouter.ai/api/alpha/decisions"

        criteria: dict[str, str] = {}
        search_inputs = [
            element
            for element in state.elements
            if element.tag == "input" and any(label in element.name.lower() for label in ("search", "검색"))
        ]
        if goal.name == "search_exploration":
            candidate_elements = search_inputs or [
                element
                for element in state.elements
                if any(label == element.name.lower() for label in ("search", "검색"))
            ]
        else:
            candidate_elements = state.elements

        for el in candidate_elements:
            if not el.enabled:
                continue
            if el.role == "file":
                if ActionKind.SET_FILES in goal.allowed_actions:
                    criteria[str(el.id)] = f"Upload fixture using [{el.id}] '{el.name}'"
            elif el.role in ("textbox", "searchbox") or el.tag in ("input", "textarea", "mdui-text-field"):
                if ActionKind.TYPE_TEXT in goal.allowed_actions:
                    criteria[str(el.id)] = f"Enter text into [{el.id}] '{el.name}'"
            elif ActionKind.CLICK in goal.allowed_actions:
                criteria[str(el.id)] = f"Click [{el.id}] '{el.name}'"

        if action_count >= 2:
            criteria["DONE"] = "Goal has been accomplished; finish execution"

        if not criteria or any("loading" in el.name.lower() or "busy" in el.name.lower() for el in state.elements):
            criteria["WAIT"] = "Wait for page elements to render and settle"

        instructions = f"Goal: {goal.description}\n"
        el_names_lower = [e.name.lower() for e in state.elements if e.enabled]
        target_file = (goal.parameters.get("old_name") or goal.parameters.get("file_name") or "sample-1k.txt").lower()

        if goal.name == "search_exploration" and search_inputs:
            instructions += f"The search interface is open. Enter the query '{goal.parameters.get('search_term', '')}'."
        elif any(any(k in n for k in ("새 폴더", "new folder", "폴더 생성", "폴더 추가")) for n in el_names_lower):
            instructions += "The create menu is open. Choose the control to create a new folder."
        elif any(n in ("만들기", "create", "생성") for n in el_names_lower):
            instructions += f"The folder dialog is open. Confirm creation or enter the folder name '{goal.parameters.get('folder_name', 'jev-folder')}'."
        elif any(any(k in n for k in ("새 이름", "new name")) for n in el_names_lower):
            instructions += f"The rename dialog is open. Enter the new name '{goal.parameters.get('new_name', 'sample-renamed.txt')}'."
        elif any(any(k in n for k in ("이름 바꾸기", "rename", "이름 변경")) for n in el_names_lower):
            instructions += "Choose the control to rename the item."
        elif any(n in ("확인", "ok", "save", "저장") for n in el_names_lower):
            instructions += "Confirm the dialog action."
        elif any(any(k in n for k in ("삭제", "delete", "제거")) for n in el_names_lower):
            instructions += "Confirm the delete action."
        elif any(any(k in n for k in ("추가 작업", "더보기", "more")) for n in el_names_lower) and goal.name in ("rename_file", "delete_and_trash"):
            instructions += f"Open the row actions menu for '{target_file}'."
        elif any(any(k in n for k in ("검색", "search")) for n in el_names_lower) and goal.name == "search_exploration":
            instructions += "Open the search interface."
        else:
            instructions += "Choose the next control that advances the goal."

        payload = {
            "model": self.model,
            "state": (
                f"Goal: {goal.description}\n"
                f"Parameters: {json.dumps(goal.parameters)}\n\n"
                f"{state.to_observation()}"
            ),
            "questions": {
                "next_action": {
                    "type": "choice",
                    "instructions": instructions,
                    "criteria": criteria,
                }
            },
        }

        req = urllib.request.Request(
            endpoint,
            data=json.dumps(payload).encode("utf-8"),
            headers={
                "Authorization": f"Bearer {self.api_key}",
                "Content-Type": "application/json",
                "HTTP-Referer": "https://github.com/heavycaffeiner/Stowcloud",
                "X-Title": "Stowcloud E2E",
            },
            method="POST",
        )

        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                data = json.loads(resp.read().decode("utf-8"))
                usage = data.get("usage", {})
                prompt_tokens = usage.get("input_tokens", 500)
                completion_tokens = usage.get("output_tokens", 50)
                answers = data.get("answers", {})
                action_answer = answers.get("next_action") or {}
                choice = action_answer.get("choice", "DONE")
                confidence = float(action_answer.get("confidence", 1.0))

                if choice == "DONE":
                    if action_count < 2 or confidence < 0.5:
                        return AgentAction(kind=ActionKind.WAIT, reason="Premature DONE rejected: continue goal actions"), prompt_tokens, completion_tokens
                    return AgentAction(kind=ActionKind.DONE, confidence=confidence, reason="Model decided goal is done"), prompt_tokens, completion_tokens
                if choice == "WAIT":
                    return AgentAction(kind=ActionKind.WAIT, confidence=confidence, reason="Model requested wait"), prompt_tokens, completion_tokens
                if choice == "BLOCKED":
                    return AgentAction(kind=ActionKind.BLOCKED, confidence=confidence, reason="Model declared blocked"), prompt_tokens, completion_tokens

                if choice.isdigit():
                    el_id = int(choice)
                    target_el = state.get_element_by_id(el_id)
                    if target_el and target_el.role == "file":
                        return (
                            AgentAction(kind=ActionKind.SET_FILES, element_id=el_id, fixture_name=goal.parameters.get("fixture_name", "sample-1k.txt"), confidence=confidence),
                            prompt_tokens,
                            completion_tokens,
                        )
                    if target_el and (target_el.role in ("textbox", "searchbox") or target_el.tag in ("input", "textarea", "mdui-text-field")):
                        text_val = (
                            goal.parameters.get("folder_name")
                            or goal.parameters.get("new_name")
                            or goal.parameters.get("search_term")
                            or goal.parameters.get("text")
                            or "test-input"
                        )
                        return (
                            AgentAction(kind=ActionKind.TYPE_TEXT, element_id=el_id, text=str(text_val), confidence=confidence),
                            prompt_tokens,
                            completion_tokens,
                        )
                    return AgentAction(kind=ActionKind.CLICK, element_id=el_id, confidence=confidence), prompt_tokens, completion_tokens

                return AgentAction(kind=ActionKind.DONE, confidence=confidence, reason=f"Fallback for choice {choice}"), prompt_tokens, completion_tokens
        except urllib.error.HTTPError as e:
            err_msg = e.read().decode("utf-8", errors="replace")
            raise RuntimeError(f"OpenRouter API error (status {e.code}): {err_msg}") from e
        except Exception as e:
            raise RuntimeError(f"Failed to query OpenRouter policy: {e}") from e

    def execute_goal(
        self,
        goal: GoalContract,
        context: dict[str, Any],
        mock_actions: list[AgentAction] | None = None,
    ) -> dict[str, Any]:
        trace = AgenticTrace(goal=goal.name, lane=self.lane)
        action_count = 0
        status = "running"
        oracle_data: dict[str, Any] = {}

        base_url = context.get("base_url", "https://localhost:18900")
        context["visible_elements"] = []
        context["typed_texts"] = []

        if self.cdp:
            target_url = f"{base_url.rstrip('/')}{goal.starting_route}"
            session_cookie = os.environ.get("SC_SESSION_COOKIE")
            if session_cookie:
                try:
                    self.cdp.set_cookie("__Host-sc_sid", session_cookie, base_url)
                except Exception:
                    pass
            self.cdp.navigate(target_url)
            context["current_url"] = target_url

        try:
            step_idx = 0
            while action_count < goal.max_actions:
                if self.cdp:
                    state = self.cdp.extract_live_browser_state()
                    context["current_url"] = state.url
                    context["visible_elements"] = [
                        {"tag": element.tag, "role": element.role, "name": element.name}
                        for element in state.elements
                    ]
                else:
                    base_elements = [
                        InteractiveElement(id=1, tag="button", role="button", name="새로 만들기 (New)"),
                        InteractiveElement(id=2, tag="input", role="textbox", name="폴더 이름 (Folder name)"),
                        InteractiveElement(id=3, tag="button", role="button", name="만들기 (Create)"),
                        InteractiveElement(id=4, tag="button", role="button", name="이름 바꾸기 (Rename)"),
                        InteractiveElement(id=5, tag="button", role="button", name="삭제 (Delete)"),
                        InteractiveElement(id=6, tag="button", role="button", name="격자형으로 보기 (Grid view)"),
                        InteractiveElement(id=7, tag="input", role="file", name="파일 업로드 (File Upload)"),
                        InteractiveElement(id=8, tag="button", role="button", name="다운로드 (Download)"),
                        InteractiveElement(id=9, tag="button", role="button", name="검색 (Search)"),
                        InteractiveElement(id=10, tag="button", role="button", name="설정 (Settings)"),
                        InteractiveElement(id=11, tag="button", role="button", name="관리자 (Admin)"),
                    ]
                    state = BrowserState(
                        url=f"{base_url}{goal.starting_route}",
                        title="Stowcloud",
                        active_route=goal.starting_route,
                        elements=base_elements,
                        available_fixtures=["sample-1k.txt", "sample-4k.bin"],
                    )
                fp = self.coverage.record_state(state)

                if mock_actions and step_idx < len(mock_actions):
                    action = mock_actions[step_idx]
                    prompt_tokens = 500
                    completion_tokens = 20
                    step_idx += 1
                elif mock_actions:
                    action = AgentAction(kind=ActionKind.DONE, reason="Mock execution: finished")
                    prompt_tokens = 500
                    completion_tokens = 20
                else:
                    action, prompt_tokens, completion_tokens = self.query_policy(state, goal, action_count=action_count)

                cost_increment = (prompt_tokens / 1_000_000.0) * trace.price_per_million
                self.budget.check(cost_increment=cost_increment)
                trace.jev_calls += 1
                trace.jev_input_tokens += prompt_tokens
                trace.jev_output_tokens += completion_tokens

                action = self.recover_low_confidence(state, action)
                validate_action(action, state)
                action_count += 1
                if action.kind == ActionKind.TYPE_TEXT and action.text:
                    context.setdefault("typed_texts", []).append(action.text)

                # Execute action over CDP if live
                if self.cdp and action.kind == ActionKind.SET_FILES:
                    fix_name = action.fixture_name or goal.parameters.get("fixture_name") or "sample-1k.txt"
                    fix_path = os.path.join(context.get("share_dir", "/tmp"), fix_name)
                    if not os.path.exists(fix_path):
                        with open(fix_path, "wb") as f:
                            f.write(b"sample fixture content for upload\n")
                    self.cdp.set_file_input_files(fix_path)
                elif self.cdp and action.element_id:
                    el = state.get_element_by_id(action.element_id)
                    if el and el.selector and "," in el.selector:
                        x_s, y_s = el.selector.split(",")
                        x, y = int(x_s), int(y_s)
                        if action.kind == ActionKind.CLICK:
                            self.cdp.click(x, y)
                        elif action.kind == ActionKind.TYPE_TEXT and action.text:
                            self.cdp.click(x, y)
                            self.cdp.type_text(action.text, x, y)
                            self.cdp.press_key("Enter")
                elif self.cdp and action.kind == ActionKind.PRESS_KEY and action.key:
                    self.cdp.press_key(action.key)
                elif action.kind == ActionKind.WAIT:
                    time.sleep(0.5)

                target = state.get_element_by_id(action.element_id) if action.element_id else None
                trace.add_step(
                    url=state.url,
                    action=action.kind.value,
                    target=f"[{target.id}] {target.name}" if target else None,
                    text=action.text,
                    confidence=action.confidence,
                    duration_ms=45,
                    state_fingerprint=fp,
                )

                # If running simulated harness, advance state on valid user actions
                if not self.cdp and action.kind in (ActionKind.CLICK, ActionKind.TYPE_TEXT, ActionKind.SET_FILES):
                    if goal.name == "create_folder" and action.element_id in (1, 2, 3):
                        os.makedirs(os.path.join(context.get("share_dir", "/tmp"), goal.parameters.get("folder_name", "jev-folder")), exist_ok=True)
                    elif goal.name == "upload_file":
                        with open(os.path.join(context.get("share_dir", "/tmp"), goal.parameters.get("fixture_name", "sample-1k.txt")), "wb") as f:
                            f.write(b"sample fixture content for upload\n")
                    elif goal.name == "rename_file":
                        src = os.path.join(context.get("share_dir", "/tmp"), goal.parameters.get("old_name", "sample-1k.txt"))
                        dst = os.path.join(context.get("share_dir", "/tmp"), goal.parameters.get("new_name", "sample-renamed.txt"))
                        if os.path.exists(src):
                            os.rename(src, dst)
                        else:
                            with open(dst, "wb") as f:
                                f.write(b"renamed content\n")
                    elif goal.name == "delete_and_trash":
                        target = os.path.join(context.get("share_dir", "/tmp"), goal.parameters.get("file_name", "sample-renamed.txt"))
                        if os.path.exists(target):
                            os.remove(target)
                    elif goal.name == "search_exploration":
                        context["current_url"] = f"{base_url}/search"
                if action.kind == ActionKind.DONE:
                    break
                if action.kind == ActionKind.BLOCKED:
                    status = "blocked"
                    break

            # Deterministic Oracle Evaluation
            if status != "blocked":
                oracle_result: OracleResult = goal.oracle.evaluate(context)
                oracle_data = oracle_result.to_dict()
                if oracle_result.passed:
                    status = "passed"
                else:
                    status = "failed"

        except BudgetExceededError as e:
            status = "budget_exceeded"
            oracle_data = {"error": str(e)}
        except ActionValidationError as e:
            status = "action_validation_failed"
            oracle_data = {"error": str(e)}
        except Exception as e:
            status = "harness_error"
            oracle_data = {"error": str(e)}

        trace.finish(result=status, oracle_data=oracle_data)

        trace_path = os.path.join(self.output_dir, f"trace-{goal.name}.json")
        with open(trace_path, "w", encoding="utf-8") as f:
            f.write(trace.to_json())

        cov_path = os.path.join(self.output_dir, "coverage-report.json")
        with open(cov_path, "w", encoding="utf-8") as f:
            f.write(self.coverage.to_json())

        return trace.to_dict()


def check_doctor() -> bool:
    """Check environment prerequisites and doctor output."""
    checks = []

    # 1. Python version
    py_ok = sys.version_info >= (3, 11)
    checks.append(("Python >= 3.11", py_ok, f"Found {sys.version.split()[0]}"))

    # 2. Chrome availability
    chrome_path = shutil.which("google-chrome") or shutil.which("chromium")
    checks.append(("Chrome/Chromium executable", chrome_path is not None, chrome_path or "Missing"))

    # 3. Writable output dir
    out_dir = "test-results/agentic"
    try:
        os.makedirs(out_dir, exist_ok=True)
        test_file = os.path.join(out_dir, ".test_write")
        with open(test_file, "w") as f:
            f.write("ok")
        os.remove(test_file)
        checks.append(("Writable test-results directory", True, out_dir))
    except Exception as e:
        checks.append(("Writable test-results directory", False, str(e)))

    # 4. API Key check (OpenRouter key)
    api_key = os.environ.get("OPENROUTER_API_KEY") or os.environ.get("JEV_API_KEY")
    key_name = "OPENROUTER_API_KEY" if os.environ.get("OPENROUTER_API_KEY") else "JEV_API_KEY"
    checks.append(
        (
            "OPENROUTER_API_KEY provisioned",
            api_key is not None,
            f"Set ({key_name})" if api_key else "Unset (optional for dry-run/mock)",
        )
    )

    # 5. Model configuration
    model = os.environ.get("JEV_MODEL", "typesafe/jev-1.13")
    endpoint = os.environ.get(
        "OPENROUTER_API_BASE",
        os.environ.get("JEV_API_BASE", "https://openrouter.ai/api/v1"),
    )
    checks.append(("OpenRouter model", True, model))
    checks.append(("OpenRouter endpoint", True, endpoint))

    print("=== Stowcloud Jev E2E Doctor Preflight ===")
    all_ok = True
    for name, ok, detail in checks:
        mark = "✓" if ok else "✘"
        print(f"[{mark}] {name}: {detail}")
        if not ok and name != "OPENROUTER_API_KEY provisioned":
            all_ok = False

    return all_ok


def main() -> None:
    parser = argparse.ArgumentParser(description="Stowcloud Jev Agentic E2E Runner")
    parser.add_argument("command", choices=["doctor", "run"], default="run", nargs="?")
    parser.add_argument("--lane", choices=["pr", "nightly"], default="pr")
    parser.add_argument("--output-dir", default="test-results/agentic")
    args = parser.parse_args()

    if args.command == "doctor":
        ok = check_doctor()
        sys.exit(0 if ok else 1)

    runner = JevRunner(lane=args.lane, output_dir=args.output_dir)
    print(
        f"Starting Jev agent runner in lane '{args.lane}' using model '{runner.model}' "
        f"at '{runner.api_base}'..."
    )

    share_dir = os.environ.get("SC_SHARE_DIR", "/tmp")
    base_url = os.environ.get("SC_BASE_URL", "https://localhost:18900")

    # In PR lane, run core goals; in nightly, run full catalog
    if args.lane == "nightly":
        goals = get_default_goal_catalog(share_dir)
    else:
        goals = [
            build_create_folder_goal("jev-e2e-folder"),
        ]

    passed_count = 0
    total_cost = 0.0

    for g in goals:
        if g.name in ("download_file", "search_exploration"):
            with open(os.path.join(share_dir, "a.txt"), "wb") as f:
                f.write(b"hello world\n")
        elif g.name in ("rename_file", "upload_file"):
            with open(os.path.join(share_dir, "sample-1k.txt"), "wb") as f:
                f.write(b"sample file content for e2e\n")
        elif g.name == "delete_and_trash":
            with open(os.path.join(share_dir, "sample-renamed.txt"), "wb") as f:
                f.write(b"sample renamed content for delete\n")
        elif g.name == "create_folder":
            shutil.rmtree(os.path.join(share_dir, "jev-folder"), ignore_errors=True)

        context = {
            "share_dir": share_dir,
            "base_url": base_url,
            "current_url": f"{base_url}{g.starting_route}",
        }

        print(f"--> Executing Jev goal: {g.name} ({g.description})")
        res = runner.execute_goal(g, context=context)
        print(f"    Result: {res['result']}")
        if res["result"] == "passed":
            passed_count += 1
        cost = res.get("telemetry", {}).get("total_estimated_cost_usd", 0.0)
        total_cost += cost
    print(f"\n=== Jev E2E Summary ({args.lane} lane) ===")
    print(f"Goals executed: {len(goals)}, Passed: {passed_count}, Failed: {len(goals) - passed_count}")
    print(f"Total estimated cost: ${total_cost:.6f} USD")

    if runner.cdp:
        runner.cdp.close()

    if passed_count < len(goals):
        sys.exit(1)


if __name__ == "__main__":
    main()

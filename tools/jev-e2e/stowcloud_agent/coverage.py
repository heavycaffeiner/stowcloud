"""Coverage-guided exploration graph and loop detection for Jev agent."""

from dataclasses import dataclass, field
import json
from typing import Any
from .browser_state import BrowserState


@dataclass
class StateNode:
    fingerprint: str
    route: str
    controls: list[str] = field(default_factory=list)
    visit_count: int = 0


@dataclass
class TransitionEdge:
    from_state: str
    to_state: str
    action: str
    target: str | None = None
    count: int = 1


class CoverageGraph:
    def __init__(self) -> None:
        self.nodes: dict[str, StateNode] = {}
        self.edges: dict[str, TransitionEdge] = {}
        self.history: list[str] = []

    def record_state(self, state: BrowserState) -> str:
        fp = state.fingerprint()
        if fp not in self.nodes:
            controls = [e.name for e in state.elements if e.name and e.enabled]
            self.nodes[fp] = StateNode(
                fingerprint=fp,
                route=state.active_route,
                controls=controls,
                visit_count=1,
            )
        else:
            self.nodes[fp].visit_count += 1

        self.history.append(fp)
        return fp

    def record_transition(
        self, from_fp: str, to_fp: str, action: str, target: str | None = None
    ) -> None:
        key = f"{from_fp}->{to_fp}:{action}:{target or ''}"
        if key in self.edges:
            self.edges[key].count += 1
        else:
            self.edges[key] = TransitionEdge(
                from_state=from_fp,
                to_state=to_fp,
                action=action,
                target=target,
                count=1,
            )

    def detect_loop(self, window_size: int = 3, threshold: int = 3) -> bool:
        """Detect if the last N states repeat in a loop."""
        if len(self.history) < window_size * threshold:
            return False

        recent = self.history[-(window_size * threshold):]
        # Check if identical pattern repeats
        pattern = recent[:window_size]
        repeats = 0
        for i in range(0, len(recent), window_size):
            if recent[i:i + window_size] == pattern:
                repeats += 1
        return repeats >= threshold

    def get_uncovered_controls(self) -> dict[str, list[str]]:
        """Return controls per state that have not yet had an outgoing edge."""
        covered_targets_by_state: dict[str, set[str]] = {}
        for edge in self.edges.values():
            if edge.target:
                covered_targets_by_state.setdefault(edge.from_state, set()).add(edge.target)

        uncovered: dict[str, list[str]] = {}
        for fp, node in self.nodes.items():
            used = covered_targets_by_state.get(fp, set())
            missing = [c for c in node.controls if c not in used]
            if missing:
                uncovered[f"{node.route} ({fp})"] = missing

        return uncovered

    def generate_report(self) -> dict[str, Any]:
        return {
            "total_states": len(self.nodes),
            "total_transitions": len(self.edges),
            "unique_routes": sorted(list({n.route for n in self.nodes.values()})),
            "uncovered_controls": self.get_uncovered_controls(),
            "nodes": [
                {
                    "fingerprint": n.fingerprint,
                    "route": n.route,
                    "controls_count": len(n.controls),
                    "visits": n.visit_count,
                }
                for n in self.nodes.values()
            ],
            "edges": [
                {
                    "from": e.from_state,
                    "to": e.to_state,
                    "action": e.action,
                    "target": e.target,
                    "count": e.count,
                }
                for e in self.edges.values()
            ],
        }

    def to_json(self, indent: int = 2) -> str:
        return json.dumps(self.generate_report(), indent=indent)

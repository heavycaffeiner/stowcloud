"""Semantic browser state extraction for Stowcloud Jev agent."""

from dataclasses import dataclass, field
import hashlib
import json
from typing import Any


@dataclass(frozen=True)
class InteractiveElement:
    id: int
    tag: str
    role: str
    name: str
    enabled: bool = True
    selector: str = ""

    def summary(self) -> str:
        status = "enabled" if self.enabled else "disabled"
        return f"[{self.id}] <{self.tag} role='{self.role}' {status}> {self.name}"


@dataclass(frozen=True)
class ScrollContainer:
    id: int
    role: str
    selector: str
    scroll_top: int = 0
    max_scroll: int = 0

    def summary(self) -> str:
        return f"[SCROLL-{self.id}] role='{self.role}' pos={self.scroll_top}/{self.max_scroll}"

@dataclass
class BrowserState:
    url: str
    title: str
    active_route: str
    elements: list[InteractiveElement] = field(default_factory=list)
    containers: list[ScrollContainer] = field(default_factory=list)
    available_fixtures: list[str] = field(default_factory=list)
    stale: bool = False

    def discover_nested_virtual_lists(self) -> list[ScrollContainer]:
        """Discover nested virtualized scroll containers."""
        virtual_containers = []
        for c in self.containers:
            if "virtual" in c.role.lower() or "grid" in c.role.lower() or "list" in c.role.lower():
                virtual_containers.append(c)
        return virtual_containers
    available_fixtures: list[str] = field(default_factory=list)

    def fingerprint(self) -> str:
        """Compute state fingerprint for coverage-guided exploration."""
        normalized_route = self.active_route.split("?")[0]
        control_names = sorted(e.name for e in self.elements if e.enabled and e.name)
        raw = f"{normalized_route}|{','.join(control_names)}"
        return hashlib.sha256(raw.encode("utf-8")).hexdigest()[:16]

    def to_observation(self) -> str:
        """Format state as compact prompt for Jev policy."""
        lines = [
            f"URL: {self.url}",
            f"Title: {self.title}",
            f"Route: {self.active_route}",
            "Interactive Elements:",
        ]
        for el in self.elements:
            lines.append(f"  {el.summary()}")

        if self.containers:
            lines.append("Scroll Containers:")
            for sc in self.containers:
                lines.append(f"  {sc.summary()}")

        if self.available_fixtures:
            lines.append(f"Available Fixtures: {', '.join(self.available_fixtures)}")

        return "\n".join(lines)

    def get_element_by_id(self, el_id: int) -> InteractiveElement | None:
        for el in self.elements:
            if el.id == el_id:
                return el
        return None

    def get_container_by_id(self, sc_id: int) -> ScrollContainer | None:
        for sc in self.containers:
            if sc.id == sc_id:
                return sc
        return None

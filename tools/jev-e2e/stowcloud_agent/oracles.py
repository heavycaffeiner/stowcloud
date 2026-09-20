"""Deterministic success oracles for Stowcloud Jev agent."""

from dataclasses import dataclass, field
import hashlib
import os
import urllib.request
import urllib.error
from typing import Any, Callable


@dataclass
class OracleResult:
    passed: bool
    reason: str
    details: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        return {
            "passed": self.passed,
            "reason": self.reason,
            "details": self.details,
        }


class DeterministicOracle:
    def evaluate(self, context: dict[str, Any]) -> OracleResult:
        raise NotImplementedError


class UrlMatchesOracle(DeterministicOracle):
    def __init__(self, expected_path_substr: str) -> None:
        self.expected_path_substr = expected_path_substr

    def evaluate(self, context: dict[str, Any]) -> OracleResult:
        current_url = context.get("current_url", "")
        if self.expected_path_substr in current_url:
            return OracleResult(
                passed=True,
                reason=f"Current URL '{current_url}' contains expected '{self.expected_path_substr}'",
                details={"url": current_url},
            )
        return OracleResult(
            passed=False,
            reason=f"URL '{current_url}' does not contain expected '{self.expected_path_substr}'",
            details={"url": current_url, "expected": self.expected_path_substr},
        )


class SearchCompletedOracle(DeterministicOracle):
    def __init__(self, query: str) -> None:
        self.query = query

    def evaluate(self, context: dict[str, Any]) -> OracleResult:
        current_url = context.get("current_url", "")
        elements = context.get("visible_elements", [])
        typed_texts = context.get("typed_texts", [])
        search_surface_visible = "/search" in current_url or any(
            element.get("tag") == "input"
            and any(label in element.get("name", "").lower() for label in ("search", "검색"))
            for element in elements
        )
        query_entered = self.query in typed_texts
        if search_surface_visible and query_entered:
            return OracleResult(
                passed=True,
                reason=f"Search surface accepted query '{self.query}'",
                details={"url": current_url, "query": self.query},
            )
        return OracleResult(
            passed=False,
            reason=f"Search surface did not accept query '{self.query}'",
            details={
                "url": current_url,
                "query_entered": query_entered,
                "search_surface_visible": search_surface_visible,
            },
        )


class ApiStatusOracle(DeterministicOracle):
    def __init__(self, endpoint_path: str, expected_status: int = 200) -> None:
        self.endpoint_path = endpoint_path
        self.expected_status = expected_status

    def evaluate(self, context: dict[str, Any]) -> OracleResult:
        base_url = context.get("base_url", "https://localhost:18900")
        url = f"{base_url.rstrip('/')}/{self.endpoint_path.lstrip('/')}"
        cookie_header = context.get("cookie_header")
        csrf_header = context.get("csrf_header")

        headers: dict[str, str] = {"Host": "localhost"}
        if cookie_header:
            headers["Cookie"] = cookie_header
        if csrf_header:
            headers["Sc-Csrf"] = csrf_header

        req = urllib.request.Request(url, headers=headers, method="GET")
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                status = resp.status
        except urllib.error.HTTPError as e:
            status = e.code
        except Exception as e:
            return OracleResult(
                passed=False,
                reason=f"Failed to query API at {url}: {e}",
            )

        if status == self.expected_status:
            return OracleResult(
                passed=True,
                reason=f"API {self.endpoint_path} returned expected status {status}",
                details={"status": status},
            )
        return OracleResult(
            passed=False,
            reason=f"API {self.endpoint_path} returned status {status}, expected {self.expected_status}",
            details={"status": status, "expected": self.expected_status},
        )


class FileExistsOnDiskOracle(DeterministicOracle):
    def __init__(self, relative_path: str, expected_hash: str | None = None) -> None:
        self.relative_path = relative_path
        self.expected_hash = expected_hash

    def evaluate(self, context: dict[str, Any]) -> OracleResult:
        share_dir = context.get("share_dir", "")
        full_path = os.path.join(share_dir, self.relative_path)
        if not os.path.exists(full_path):
            return OracleResult(
                passed=False,
                reason=f"File {self.relative_path} does not exist at {full_path}",
            )

        if self.expected_hash:
            with open(full_path, "rb") as f:
                content = f.read()

            actual_hash = hashlib.sha256(content).hexdigest()
            if actual_hash != self.expected_hash:
                return OracleResult(
                    passed=False,
                    reason=(
                        f"Hash mismatch for {self.relative_path}: expected "
                        f"{self.expected_hash}, got {actual_hash}"
                    ),
                    details={"expected": self.expected_hash, "actual": actual_hash},
                )

        return OracleResult(
            passed=True,
            reason=f"File {self.relative_path} exists with verified content",
            details={"path": full_path, "size": os.path.getsize(full_path)},
        )

class AnyFileExistsOnDiskOracle(DeterministicOracle):
    def __init__(self, relative_paths: list[str]) -> None:
        self.relative_paths = relative_paths

    def evaluate(self, context: dict[str, Any]) -> OracleResult:
        share_dir = context.get("share_dir", "")
        for p in self.relative_paths:
            full_path = os.path.join(share_dir, p)
            if os.path.exists(full_path):
                return OracleResult(
                    passed=True,
                    reason=f"Found created folder/file at {full_path}",
                    details={"path": full_path},
                )
        return OracleResult(
            passed=False,
            reason=f"None of {self.relative_paths} exist in {share_dir}",
        )


class FileNotExistsOnDiskOracle(DeterministicOracle):
    def __init__(self, relative_path: str) -> None:
        self.relative_path = relative_path

    def evaluate(self, context: dict[str, Any]) -> OracleResult:
        share_dir = context.get("share_dir", "")
        full_path = os.path.join(share_dir, self.relative_path)
        if os.path.exists(full_path):
            return OracleResult(
                passed=False,
                reason=f"File {self.relative_path} unexpectedly exists at {full_path}",
            )
        return OracleResult(
            passed=True,
            reason=f"File {self.relative_path} does not exist as expected",
        )


class CompoundOracle(DeterministicOracle):
    def __init__(self, oracles: list[DeterministicOracle], name: str = "Compound") -> None:
        self.oracles = oracles
        self.name = name

    def evaluate(self, context: dict[str, Any]) -> OracleResult:
        results = []
        for oracle in self.oracles:
            res = oracle.evaluate(context)
            results.append(res)
            if not res.passed:
                return OracleResult(
                    passed=False,
                    reason=f"{self.name} failed at sub-oracle: {res.reason}",
                    details={"results": [r.to_dict() for r in results]},
                )

        return OracleResult(
            passed=True,
            reason=f"{self.name} passed all {len(self.oracles)} oracle checks",
            details={"results": [r.to_dict() for r in results]},
        )

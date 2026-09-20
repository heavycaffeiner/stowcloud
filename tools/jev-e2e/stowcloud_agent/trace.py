"""Structured sanitized trace recording for Stowcloud Jev agent."""

from dataclasses import dataclass, field
import json
import re
import time
from typing import Any

# Sensitive pattern sanitization
SECRET_PATTERNS = [
    re.compile(r"Bearer\s+[A-Za-z0-9_\-\.]+", re.IGNORECASE),
    re.compile(r"token=[A-Za-z0-9_\-]+", re.IGNORECASE),
    re.compile(r"password=[^&\s]+", re.IGNORECASE),
    re.compile(r"csrf=[A-Za-z0-9_\-]+", re.IGNORECASE),
    re.compile(r"[0-9a-f]{64}", re.IGNORECASE),  # 32-byte hex tokens
]


def sanitize_text(text: str | None) -> str | None:
    if not text:
        return text
    sanitized = text
    for pat in SECRET_PATTERNS:
        sanitized = pat.sub("[REDACTED]", sanitized)
    return sanitized


@dataclass
class TraceStep:
    step_number: int
    url: str
    action: str
    target: str | None = None
    text: str | None = None
    confidence: float = 1.0
    duration_ms: int = 0
    state_fingerprint: str = ""

    def to_dict(self) -> dict[str, Any]:
        return {
            "step": self.step_number,
            "url": sanitize_text(self.url),
            "action": self.action,
            "target": sanitize_text(self.target),
            "text": sanitize_text(self.text),
            "confidence": round(self.confidence, 3),
            "duration_ms": self.duration_ms,
            "state_fingerprint": self.state_fingerprint,
        }


@dataclass
class AgenticTrace:
    goal: str
    lane: str = "pr"
    result: str = "running"
    steps: list[TraceStep] = field(default_factory=list)
    coverage_states: list[str] = field(default_factory=list)
    oracle_results: dict[str, Any] = field(default_factory=dict)
    jev_calls: int = 0
    jev_input_tokens: int = 0
    jev_output_tokens: int = 0
    price_per_million: float = 0.042
    helper_model_cost_usd: float = 0.0
    start_time: float = field(default_factory=time.time)
    end_time: float | None = None

    @property
    def estimated_jev_cost_usd(self) -> float:
        return (self.jev_input_tokens / 1_000_000.0) * self.price_per_million

    @property
    def total_cost_usd(self) -> float:
        return self.estimated_jev_cost_usd + self.helper_model_cost_usd

    @property
    def duration_seconds(self) -> float:
        end = self.end_time or time.time()
        return round(end - self.start_time, 2)

    def add_step(
        self,
        url: str,
        action: str,
        target: str | None = None,
        text: str | None = None,
        confidence: float = 1.0,
        duration_ms: int = 0,
        state_fingerprint: str = "",
    ) -> None:
        step_number = len(self.steps) + 1
        self.steps.append(
            TraceStep(
                step_number=step_number,
                url=url,
                action=action,
                target=target,
                text=text,
                confidence=confidence,
                duration_ms=duration_ms,
                state_fingerprint=state_fingerprint,
            )
        )
        if state_fingerprint and state_fingerprint not in self.coverage_states:
            self.coverage_states.append(state_fingerprint)

    def finish(self, result: str, oracle_data: dict[str, Any] | None = None) -> None:
        self.result = result
        self.end_time = time.time()
        if oracle_data:
            self.oracle_results = oracle_data

    def to_dict(self) -> dict[str, Any]:
        return {
            "goal": self.goal,
            "lane": self.lane,
            "result": self.result,
            "duration_seconds": self.duration_seconds,
            "telemetry": {
                "jev_calls": self.jev_calls,
                "jev_input_tokens": self.jev_input_tokens,
                "jev_output_tokens": self.jev_output_tokens,
                "price_per_million": self.price_per_million,
                "estimated_jev_cost_usd": round(self.estimated_jev_cost_usd, 6),
                "helper_model_cost_usd": round(self.helper_model_cost_usd, 6),
                "total_estimated_cost_usd": round(self.total_cost_usd, 6),
                "cost_per_successful_goal": (
                    round(self.total_cost_usd, 6) if self.result == "passed" else None
                ),
            },
            "steps": [s.to_dict() for s in self.steps],
            "coverage": self.coverage_states,
            "oracle": self.oracle_results,
        }

    def to_json(self, indent: int = 2) -> str:
        return json.dumps(self.to_dict(), indent=indent)

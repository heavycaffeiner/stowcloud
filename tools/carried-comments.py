#!/usr/bin/env python3
"""Report comment lines in the new engine tree that also appear in the old tree.

The rebuild rule forbids carrying a comment over from go/internal. This finds
the violations by exact line match, ignoring indentation and trailing space.
Short lines are excluded: a fragment like "// The store." is common phrasing
rather than carried prose.
"""
import sys
from pathlib import Path

MIN_LEN = 30
ROOT = Path(__file__).resolve().parents[1] / "go"


def comment_lines(path):
    """Yield (lineno, normalized text) for each // comment line."""
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return
    for i, line in enumerate(text.splitlines(), 1):
        stripped = line.strip()
        if not stripped.startswith("//"):
            continue
        body = stripped[2:].strip()
        if len(body) >= MIN_LEN:
            yield i, body


def collect(root):
    out = {}
    for path in sorted(root.rglob("*.go")):
        for lineno, body in comment_lines(path):
            out.setdefault(body, []).append((path, lineno))
    return out


def main():
    old = collect(ROOT / "internal")
    targets = sys.argv[1:] or ["engine"]

    total = 0
    per_file = {}
    for target in targets:
        for path in sorted((ROOT / target).rglob("*.go")):
            for lineno, body in comment_lines(path):
                if body in old:
                    per_file.setdefault(path, []).append((lineno, body))
                    total += 1

    for path in sorted(per_file):
        rel = path.relative_to(ROOT)
        print(f"\n{rel}  ({len(per_file[path])} carried lines)")
        for lineno, body in per_file[path]:
            print(f"  {lineno:5d}  {body}")

    print(f"\n{total} carried comment lines across {len(per_file)} files")
    return 1 if total else 0


if __name__ == "__main__":
    sys.exit(main())

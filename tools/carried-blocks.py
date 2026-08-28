#!/usr/bin/env python3
"""Print the comment blocks in one file that contain carried lines.

A block is a run of consecutive // lines. Printing whole blocks rather than
single lines gives the rewriter the full thought to restate, instead of an
isolated sentence out of its context.
"""
import sys
from pathlib import Path

MIN_LEN = 30
ROOT = Path("/home/hyun/Projects/Stowcloud/go")


def comment_lines(path):
    for i, line in enumerate(path.read_text(errors="replace").splitlines(), 1):
        stripped = line.strip()
        if stripped.startswith("//"):
            body = stripped[2:].strip()
            if len(body) >= MIN_LEN:
                yield i, body


def old_bodies():
    out = set()
    for path in (ROOT / "internal").rglob("*.go"):
        for _, body in comment_lines(path):
            out.add(body)
    return out


def main():
    old = old_bodies()
    for arg in sys.argv[1:]:
        path = ROOT / arg
        lines = path.read_text().splitlines()
        carried = {ln for ln, b in comment_lines(path) if b in old}

        blocks, cur = [], []
        for i, line in enumerate(lines, 1):
            if line.strip().startswith("//"):
                cur.append(i)
                continue
            if cur and any(x in carried for x in cur):
                blocks.append(cur)
            cur = []
        if cur and any(x in carried for x in cur):
            blocks.append(cur)

        print(f"### {arg}: {len(blocks)} blocks, {len(carried)} carried lines")
        for b in blocks:
            print(f"--- {b[0]}-{b[-1]}")
            for ln in b:
                print(lines[ln - 1])
            print()


main()

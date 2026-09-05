#!/usr/bin/env bash
# Create a verified implementation checkpoint.
#   bash scripts/checkpoint.sh "<subject>" [crate...]
# Runs the verification gate for the given crates (or the whole workspace),
# and only commits if it passes.  Use --force to snapshot a failing state.
set -uo pipefail
cd "$(dirname "$0")/.."

FORCE=0
if [ "${1:-}" = "--force" ]; then FORCE=1; shift; fi
SUBJECT="${1:?usage: checkpoint.sh [--force] \"<subject>\" [crate...]}"; shift

if bash scripts/verify.sh "$@"; then
  STATUS="verified"
elif [ "$FORCE" = 1 ]; then
  STATUS="WIP (verification failing)"
else
  echo "verification failed; not committing.  re-run with --force to snapshot anyway." >&2
  exit 1
fi

git add -A
if git diff --cached --quiet; then echo "nothing to commit"; exit 0; fi

STAT=$(git diff --cached --shortstat)
git commit -q -m "$SUBJECT

Scope: ${*:-workspace}
Gate:  $STATUS
Diff: $STAT"
git log --oneline -1

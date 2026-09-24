#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$root_dir/checks/consumers/web"

corepack pnpm install --frozen-lockfile

resolved="$(corepack pnpm list --depth 0 --json | node -e '
let input = "";
process.stdin.on("data", chunk => { input += chunk; });
process.stdin.on("end", () => {
  const root = JSON.parse(input)[0];
  const dependency = (root.dependencies || {})["@stowcloud/rclone-crypt"];
  process.stdout.write(dependency?.version || "");
});
')"
test "$resolved" = "0.1.0"

grep -Fq 'github:Stowcloud/rclone-crypt#v0.1.0' package.json
grep -Fq 'https://codeload.github.com/Stowcloud/rclone-crypt/tar.gz/eb48c44602f568a783b5b7aa34c02ee7ea55b09d' pnpm-lock.yaml
! grep -Eq '(^|[[:space:]])(link:|file:|workspace:|replace)' pnpm-lock.yaml

corepack pnpm build
corepack pnpm check
corepack pnpm test

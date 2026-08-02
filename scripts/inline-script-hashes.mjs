#!/usr/bin/env node
// Prints the CSP `script-src` hash sources for every inline (no `src=`)
// `<script>` in an HTML file, one space-joined line: 'sha256-xxx' 'sha256-yyy'
//
// This must byte-for-byte match `inline_script_bodies` /
// `inline_script_csp_sources` in crates/sc-http/src/lib.rs — it exists so
// `scripts/deploy.sh` can compute the same hash from outside the server
// process and compare it against what the server actually advertises. A
// mismatch there is exactly the incident this check exists for: filenames
// matched but the served inline bootstrap and the CSP hash came from
// different builds, producing a blank page with no console error louder
// than a CSP violation.
//
//   node scripts/inline-script-hashes.mjs <html-file>
import fs from 'node:fs';
import crypto from 'node:crypto';

const html = fs.readFileSync(process.argv[2], 'utf8');
const lower = html.toLowerCase();
const hashes = [];
let idx = 0;
for (;;) {
  const rel = lower.indexOf('<script', idx);
  if (rel === -1) break;
  const gt = lower.indexOf('>', rel);
  if (gt === -1) break;
  const hasSrc = lower.slice(rel, gt).includes('src=');
  const contentStart = gt + 1;
  const close = lower.indexOf('</script>', contentStart);
  if (close === -1) break;
  if (!hasSrc) {
    const body = html.slice(contentStart, close);
    const digest = crypto.createHash('sha256').update(body, 'utf8').digest('base64');
    hashes.push(`'sha256-${digest}'`);
  }
  idx = close + '</script>'.length;
}
console.log(hashes.join(' '));

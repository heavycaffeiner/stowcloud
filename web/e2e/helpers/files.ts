import * as fs from 'node:fs';
import * as path from 'node:path';
import * as os from 'node:os';
import { sha256 } from './hashes';

export function generateDeterministicBytes(size: number, seed = 42): Buffer {
  const buf = Buffer.alloc(size);
  let state = seed;
  for (let i = 0; i < size; i++) {
    state = (state * 1664525 + 1013904223) >>> 0;
    buf[i] = state & 0xff;
  }
  return buf;
}

export function createTempFixtureFile(
  name: string,
  size: number,
  targetDir?: string,
): {
  filePath: string;
  buffer: Buffer;
  hash: string;
  cleanup: () => void;
} {
  const dir = targetDir || fs.mkdtempSync(path.join(os.tmpdir(), 'sc-fixture-'));
  const filePath = path.join(dir, name);
  const buffer = generateDeterministicBytes(size);
  fs.writeFileSync(filePath, buffer);
  const hash = sha256(buffer);

  return {
    filePath,
    buffer,
    hash,
    cleanup: () => {
      try {
        fs.rmSync(filePath, { force: true });
        if (!targetDir) {
          fs.rmSync(dir, { recursive: true, force: true });
        }
      } catch {
        // ignore cleanup error
      }
    },
  };
}

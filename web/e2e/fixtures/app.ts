import { test as base, expect, type ConsoleMessage, type Request } from '@playwright/test';
import { spawn, execFileSync, execSync, type ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import * as net from 'node:net';
import * as https from 'node:https';
import { fileURLToPath } from 'node:url';

export interface WorkerApp {
  baseURL: string;
  port: number;
  dataDir: string;
  shareDir: string;
  setupToken: string;
  binPath: string;
  logPath: string;
  adminUser: { id: string; name: string; pass: string };
  setupAdmin: () => Promise<{ id: string; name: string; pass: string }>;
}

export interface TestNamespace {
  (prefix?: string): string;
}

export interface ArtifactCollector {
  pageErrors: Error[];
  consoleErrors: string[];
  failedRequests: { url: string; errorText: string }[];
  clear: () => void;
  assertClean: () => void;
}

let cachedBinPath: string | null = null;

function resolveBinary(): string {
  if (process.env.SC_TEST_BIN && fs.existsSync(process.env.SC_TEST_BIN)) {
    return process.env.SC_TEST_BIN;
  }
  if (cachedBinPath && fs.existsSync(cachedBinPath)) {
    return cachedBinPath;
  }
  const repoRoot = fileURLToPath(new URL('../../..', import.meta.url));
  const tempBin = path.join(os.tmpdir(), 'stowcloud-e2e-sc-engine');
  if (fs.existsSync(tempBin)) {
    cachedBinPath = tempBin;
    return tempBin;
  }

  execSync('go build -tags embed_ui -o ' + JSON.stringify(tempBin) + ' ./cmd/sc-engine', {
    cwd: path.join(repoRoot, 'go'),
    env: { ...process.env, CGO_ENABLED: '0', GOOS: 'linux' },
    stdio: 'pipe',
  });
  cachedBinPath = tempBin;
  return tempBin;
}

function findAvailablePort(): Promise<number> {
  const { promise, resolve, reject } = Promise.withResolvers<number>();
  const srv = net.createServer();
  srv.listen(0, '127.0.0.1', () => {
    const addr = srv.address();
    const port = typeof addr === 'object' && addr ? addr.port : 0;
    srv.close((err) => (err ? reject(err) : resolve(port)));
  });
  return promise;
}

function pollHealth(port: number, deadlineMs: number): Promise<boolean> {
  const { promise, resolve } = Promise.withResolvers<boolean>();
  const start = Date.now();
  function attempt() {
    if (Date.now() - start > deadlineMs) {
      resolve(false);
      return;
    }
    const req = https.request(
      {
        hostname: '127.0.0.1',
        port,
        path: '/api/v1/system/health',
        method: 'GET',
        headers: { Host: 'localhost' },
        servername: 'localhost',
        rejectUnauthorized: false,
        timeout: 1000,
      },
      (res) => {
        if (res.statusCode === 200) {
          resolve(true);
        } else {
          setTimeout(attempt, 150);
        }
      },
    );
    req.on('error', () => {
      setTimeout(attempt, 150);
    });
    req.on('timeout', () => {
      req.destroy();
      setTimeout(attempt, 150);
    });
    req.end();
  }
  attempt();
  return promise;
}

export const test = base.extend({
  workerApp: [
    async ({}, use, workerInfo) => {
      const binPath = resolveBinary();
      const port = await findAvailablePort();
      const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), `sc-e2e-data-w${workerInfo.workerIndex}-`));
      const shareDir = fs.mkdtempSync(path.join(os.tmpdir(), `sc-e2e-share-w${workerInfo.workerIndex}-`));
      const logPath = path.join(dataDir, 'server.log');

      fs.mkdirSync(path.join(shareDir, 'sub'), { recursive: true });
      fs.writeFileSync(path.join(shareDir, 'a.txt'), 'hello\n');
      fs.writeFileSync(path.join(shareDir, 'sub', 'b.txt'), 'world\n');

      const seed = (section: string, payload: object) => {
        execFileSync(binPath, ['settings', 'set', section, '--data-dir', dataDir], {
          input: JSON.stringify(payload),
          stdio: ['pipe', 'ignore', 'pipe'],
        });
      };

      seed('network', { bind: `127.0.0.1:${port}`, app_hosts: ['localhost', '127.0.0.1'] });
      seed('rate', { per_sec: 2000, burst: 5000 });
      seed('security', { hardening: 'off' });

      const logFd = fs.openSync(logPath, 'w');
      const proc: ChildProcess = spawn(binPath, ['-data', dataDir], {
        stdio: ['ignore', logFd, logFd],
      });

      const ready = await pollHealth(port, 30000);
      if (!ready) {
        proc.kill('SIGKILL');
        fs.closeSync(logFd);
        const logContent = fs.existsSync(logPath) ? fs.readFileSync(logPath, 'utf8') : 'no log';
        throw new Error(`sc-engine failed to become healthy on port ${port}. Log:\n${logContent}`);
      }

      let setupToken = '';
      const tokenPath = path.join(dataDir, 'setup-token');
      if (fs.existsSync(tokenPath)) {
        setupToken = fs.readFileSync(tokenPath, 'utf8').trim();
      }

      const baseURL = `https://localhost:${port}`;
      let adminCached: { id: string; name: string; pass: string } | null = null;

      const workerApp: WorkerApp = {
        baseURL,
        port,
        dataDir,
        shareDir,
        setupToken,
        binPath,
        logPath,
        get adminUser() {
          if (!adminCached) {
            throw new Error('adminUser accessed before setupAdmin() called');
          }
          return adminCached;
        },
        setupAdmin: async () => {
          if (adminCached) return adminCached;
          const name = `admin-w${workerInfo.workerIndex}`;
          const pass = 'Password123!';
          const { promise, resolve, reject } = Promise.withResolvers<{ id: string; name: string; pass: string }>();

          const req = https.request(
            {
              hostname: '127.0.0.1',
              port,
              path: '/api/v1/system/setup',
              method: 'POST',
              headers: {
                'Content-Type': 'application/json',
                Host: 'localhost',
              },
              servername: 'localhost',
              rejectUnauthorized: false,
            },
            (res) => {
              let body = '';
              res.on('data', (chunk) => {
                body += chunk;
              });
              res.on('end', () => {
                if (res.statusCode !== 200 && res.statusCode !== 201) {
                  reject(new Error(`Failed to setup admin user: status ${res.statusCode} ${body}`));
                  return;
                }
                try {
                  const data = JSON.parse(body) as { user?: { id: string } };
                  adminCached = { id: String(data.user?.id || '1'), name, pass };
                  resolve(adminCached);
                } catch (e) {
                  reject(e);
                }
              });
            },
          );
          req.on('error', reject);
          req.write(
            JSON.stringify({
              token: setupToken,
              username: name,
              password: pass,
              app_hosts: ['localhost', '127.0.0.1'],
              trusted_proxies: [],
            }),
          );
          req.end();

          return promise;
        },
      };

      await use(workerApp);

      try {
        proc.kill('SIGTERM');
        const { promise: delayPromise, resolve: delayResolve } = Promise.withResolvers<void>();
        setTimeout(delayResolve, 200);
        await delayPromise;
        if (!proc.killed) {
          proc.kill('SIGKILL');
        }
      } catch {
        // ignore process kill errors
      }
      try {
        fs.closeSync(logFd);
      } catch {
        // ignore fd close errors
      }
      try {
        fs.rmSync(dataDir, { recursive: true, force: true });
        fs.rmSync(shareDir, { recursive: true, force: true });
      } catch {
        // ignore temp removal errors
      }
    },
    { scope: 'worker' },
  ],

  baseURL: async ({ workerApp }, use) => {
    await use(workerApp.baseURL);
  },

  namespace: async ({}, use, testInfo) => {
    let counter = 0;
    await use((prefix = 'item') => {
      counter++;
      const shortPrefix = prefix.slice(0, 8);
      const rand = Math.floor(Math.random() * 900 + 100);
      return `${shortPrefix}-w${testInfo.workerIndex}-${counter}-${rand}`;
    });
  },

  artifacts: async ({ page, workerApp }, use, testInfo) => {
    const pageErrors: Error[] = [];
    const consoleErrors: string[] = [];
    const failedRequests: { url: string; errorText: string }[] = [];

    const isSelfSignedWorkerRefusal = (text: string) =>
      /register a ServiceWorker/i.test(text) && /certificate/i.test(text);

    const isBenignConsoleError = (text: string) =>
      isSelfSignedWorkerRefusal(text) ||
      /SSL certificate error/i.test(text) ||
      /Failed to load resource: the server responded with a status of (?:401|404)/i.test(text);

    page.on('pageerror', (err: Error) => {
      if (!isSelfSignedWorkerRefusal(err.message) && !/SSL certificate error/i.test(err.message)) {
        pageErrors.push(err);
      }
    });
    page.on('console', (msg: ConsoleMessage) => {
      if (msg.type() === 'error') {
        const text = msg.text();
        if (!isBenignConsoleError(text)) {
          consoleErrors.push(text);
        }
      }
    });
    page.on('requestfailed', (req: Request) => {
      failedRequests.push({
        url: req.url(),
        errorText: req.failure()?.errorText || 'unknown error',
      });
    });

    const collector: ArtifactCollector = {
      pageErrors,
      consoleErrors,
      failedRequests,
      clear: () => {
        pageErrors.length = 0;
        consoleErrors.length = 0;
        failedRequests.length = 0;
      },
      assertClean: () => {
        if (pageErrors.length > 0) {
          throw new Error(`Unexpected page errors: ${pageErrors.map((e) => e.message).join('; ')}`);
        }
        if (consoleErrors.length > 0) {
          throw new Error(`Unexpected console errors: ${consoleErrors.join('; ')}`);
        }
      },
    };

    await use(collector);

    if (testInfo.status !== testInfo.expectedStatus && fs.existsSync(workerApp.logPath)) {
      const logContent = fs.readFileSync(workerApp.logPath, 'utf8');
      await testInfo.attach('server.log', {
        body: logContent,
        contentType: 'text/plain',
      });
    }
  },
});

export { expect };

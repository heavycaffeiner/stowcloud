import { test as baseTest, expect, type Page } from './app';
import * as https from 'node:https';

export interface ApiResponse<T = unknown> {
  status: number;
  headers: Record<string, string>;
  data: T;
  rawText: string;
}

export class ApiClient {
  private baseURL: string;
  private cookies: Map<string, string> = new Map();
  private csrfToken = '';

  constructor(baseURL: string) {
    this.baseURL = baseURL;
  }

  get cookiesHeader(): string {
    return Array.from(this.cookies.entries())
      .map(([k, v]) => `${k}=${v}`)
      .join('; ');
  }

  get csrf(): string {
    return this.csrfToken;
  }

  private request<T = unknown>(
    method: string,
    reqPath: string,
    body?: unknown,
  ): Promise<ApiResponse<T>> {
    const { promise, resolve, reject } = Promise.withResolvers<ApiResponse<T>>();
    const url = new URL(reqPath, this.baseURL);

    const headers: Record<string, string> = {
      Host: url.host || 'localhost',
      Origin: `${url.protocol}//${url.host}`,
    };
    if (this.cookies.size > 0) {
      headers['Cookie'] = this.cookiesHeader;
    }
    if (this.csrfToken) {
      headers['Sc-Csrf'] = this.csrfToken;
    }

    let payload: string | undefined;
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      payload = JSON.stringify(body);
      headers['Content-Length'] = String(Buffer.byteLength(payload));
    }

    const req = https.request(
      {
        hostname: url.hostname,
        port: url.port,
        path: url.pathname + url.search,
        method,
        headers,
        servername: url.hostname === '127.0.0.1' ? 'localhost' : url.hostname,
        rejectUnauthorized: false,
      },
      (res) => {
        const setCookies = res.headers['set-cookie'];
        if (setCookies) {
          for (const raw of setCookies) {
            const part = raw.split(';')[0];
            const eq = part.indexOf('=');
            if (eq > 0) {
              const name = part.slice(0, eq).trim();
              const val = part.slice(eq + 1).trim();
              this.cookies.set(name, val);
            }
          }
        }

        let rawText = '';
        res.on('data', (chunk) => {
          rawText += chunk;
        });
        res.on('end', () => {
          let data = rawText as unknown as T;
          if (rawText) {
            try {
              data = JSON.parse(rawText) as T;
            } catch {
              // keep as raw text
            }
          }

          const responseHeaders: Record<string, string> = {};
          for (const [k, v] of Object.entries(res.headers)) {
            if (typeof v === 'string') {
              responseHeaders[k.toLowerCase()] = v;
            }
          }

          resolve({
            status: res.statusCode || 0,
            headers: responseHeaders,
            data,
            rawText,
          });
        });
      },
    );

    req.on('error', reject);
    if (payload) {
      req.write(payload);
    }
    req.end();

    return promise;
  }

  async get<T = unknown>(path: string): Promise<ApiResponse<T>> {
    return this.request<T>('GET', path);
  }

  async post<T = unknown>(path: string, body?: unknown): Promise<ApiResponse<T>> {
    return this.request<T>('POST', path, body);
  }

  async patch<T = unknown>(path: string, body?: unknown): Promise<ApiResponse<T>> {
    return this.request<T>('PATCH', path, body);
  }

  async delete<T = unknown>(path: string): Promise<ApiResponse<T>> {
    return this.request<T>('DELETE', path);
  }

  async login(login: string, pass: string): Promise<ApiResponse<{ id?: string; csrf?: string }>> {
    const res = await this.post<{ id?: string; csrf?: string }>('/api/v1/auth/login', {
      login,
      password: pass,
    });
    if (res.status === 200 && res.data.csrf) {
      this.csrfToken = res.data.csrf;
    }
    return res;
  }

  async fetchSession(): Promise<ApiResponse<{ id?: string; csrf?: string; user?: unknown }>> {
    const res = await this.get<{ id?: string; csrf?: string; user?: unknown }>('/api/v1/auth/session');
    if (res.status === 200 && res.data.csrf) {
      this.csrfToken = res.data.csrf;
    }
    return res;
  }

  getPlaywrightCookies(baseURL: string) {
    const url = new URL(baseURL);
    return Array.from(this.cookies.entries()).map(([name, value]) => ({
      name,
      value,
      domain: url.hostname,
      path: '/',
      httpOnly: false,
      secure: url.protocol === 'https:',
      sameSite: 'Lax' as const,
    }));
  }
}

export const test = baseTest.extend<{
  api: ApiClient;
  authedPage: Page;
}>({
  api: async ({ workerApp }, use) => {
    await workerApp.setupAdmin();
    const client = new ApiClient(workerApp.baseURL);

    const workerRecord = workerApp as unknown as Record<string, unknown>;
    const cached = workerRecord['_adminSession'] as { cookies: Map<string, string>; csrf: string } | undefined;

    if (!cached) {
      const loginRes = await client.login(workerApp.adminUser.name, workerApp.adminUser.pass);
      if (loginRes.status !== 200) {
        throw new Error(`Failed to login admin for api fixture: ${loginRes.status} ${loginRes.rawText}`);
      }
      workerRecord['_adminSession'] = {
        cookies: new Map(client['cookies']),
        csrf: client.csrf,
      };
    } else {
      client['cookies'] = new Map(cached.cookies);
      client['csrfToken'] = cached.csrf;
    }

    await use(client);
  },

  authedPage: async ({ page, workerApp, context, api }, use) => {
    await context.addCookies(api.getPlaywrightCookies(workerApp.baseURL));
    await use(page);
  },
});

export { expect };

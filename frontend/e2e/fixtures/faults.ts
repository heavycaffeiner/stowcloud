import type { Page, Route } from '@playwright/test';

export class FaultsFixture {
  private page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  async failRequests(
    pattern: string | RegExp,
    status: number,
    options?: {
      body?: string | object;
      headers?: Record<string, string>;
      times?: number;
    },
  ): Promise<() => Promise<void>> {
    let remaining = options?.times;
    const handler = async (route: Route) => {
      if (remaining !== undefined) {
        if (remaining <= 0) {
          await route.continue();
          return;
        }
        remaining--;
      }

      const bodyStr =
        typeof options?.body === 'object'
          ? JSON.stringify(options.body)
          : options?.body ?? `Fault injected: status ${status}`;

      await route.fulfill({
        status,
        headers: {
          'Content-Type': 'application/json',
          ...(options?.headers || {}),
        },
        body: bodyStr,
      });
    };

    await this.page.route(pattern, handler);
    return async () => {
      await this.page.unroute(pattern, handler);
    };
  }

  async abortRequests(
    pattern: string | RegExp,
    errorCode: 'failed' | 'aborted' | 'timedout' | 'connectionreset' = 'connectionreset',
    times?: number,
  ): Promise<() => Promise<void>> {
    let remaining = times;
    const handler = async (route: Route) => {
      if (remaining !== undefined) {
        if (remaining <= 0) {
          await route.continue();
          return;
        }
        remaining--;
      }
      await route.abort(errorCode);
    };

    await this.page.route(pattern, handler);
    return async () => {
      await this.page.unroute(pattern, handler);
    };
  }
}

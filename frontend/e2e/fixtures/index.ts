import { test as authBaseTest, expect, type Page } from './auth';
import { AccountsFixture } from './accounts';
import { FilesystemFixture } from './filesystem';
import { PermissionsFixture } from './permissions';
import { FaultsFixture } from './faults';

export const test = authBaseTest.extend<{
  accounts: AccountsFixture;
  filesystem: FilesystemFixture;
  grants: PermissionsFixture;
  faults: FaultsFixture;
}>({
  accounts: async ({ api }, use) => {
    await use(new AccountsFixture(api));
  },
  filesystem: async ({ api }, use) => {
    await use(new FilesystemFixture(api));
  },
  grants: async ({ api }, use) => {
    await use(new PermissionsFixture(api));
  },
  faults: async ({ page }, use) => {
    await use(new FaultsFixture(page));
  },
});

export { expect };
export * from './app';
export * from './auth';
export * from './accounts';
export * from './filesystem';
export * from './permissions';
export * from './faults';

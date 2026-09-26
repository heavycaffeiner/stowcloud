import type { ApiClient } from './auth';

export interface UserRecord {
  id: string;
  name: string;
  login?: string;
  admin?: boolean;
  disabled?: boolean;
}

export interface GroupRecord {
  id: string;
  name: string;
  members?: string[];
}

export class AccountsFixture {
  private api: ApiClient;

  constructor(api: ApiClient) {
    this.api = api;
  }

  async createUser(
    login: string,
    password = 'Password123!',
    options?: { admin?: boolean },
  ): Promise<UserRecord> {
    const res = await this.api.post<{ user?: UserRecord; id?: string }>('/api/v1/admin/users', {
      login,
      display: login,
      password,
    });
    if (res.status !== 200 && res.status !== 201) {
      throw new Error(`Failed to create user ${login}: ${res.status} ${res.rawText}`);
    }
    const user = res.data.user || {
      id: String(res.data.id || ''),
      name: login,
      login,
      admin: options?.admin ?? false,
    };
    return user;
  }

  async listUsers(): Promise<UserRecord[]> {
    const res = await this.api.get<{ users?: UserRecord[] } | UserRecord[]>('/api/v1/admin/users');
    if (res.status !== 200) {
      throw new Error(`Failed to list users: ${res.status} ${res.rawText}`);
    }
    if (Array.isArray(res.data)) {
      return res.data;
    }
    return res.data.users || [];
  }

  async deleteUser(id: string): Promise<void> {
    const res = await this.api.delete(`/api/v1/admin/users/${id}`);
    if (res.status !== 200 && res.status !== 204) {
      throw new Error(`Failed to delete user ${id}: ${res.status} ${res.rawText}`);
    }
  }

  async createGroup(name: string): Promise<GroupRecord> {
    const res = await this.api.post<GroupRecord>('/api/v1/admin/groups', { name });
    if (res.status !== 200 && res.status !== 201) {
      throw new Error(`Failed to create group ${name}: ${res.status} ${res.rawText}`);
    }
    return res.data;
  }

  async listGroups(): Promise<GroupRecord[]> {
    const res = await this.api.get<{ groups?: GroupRecord[] } | GroupRecord[]>('/api/v1/admin/groups');
    if (res.status !== 200) {
      throw new Error(`Failed to list groups: ${res.status} ${res.rawText}`);
    }
    if (Array.isArray(res.data)) {
      return res.data;
    }
    return res.data.groups || [];
  }

  async deleteGroup(id: string): Promise<void> {
    const res = await this.api.delete(`/api/v1/admin/groups/${id}`);
    if (res.status !== 200 && res.status !== 204) {
      throw new Error(`Failed to delete group ${id}: ${res.status} ${res.rawText}`);
    }
  }
}

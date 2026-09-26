import type { ApiClient } from './auth';

export interface GrantRecord {
  id: string;
  user?: string;
  group?: string;
  share: string;
  allow: string[];
  deny: string[];
  inherit?: boolean;
  label?: string;
  subpath?: string;
}

export class PermissionsFixture {
  private api: ApiClient;

  constructor(api: ApiClient) {
    this.api = api;
  }

  async createGrant(params: {
    user?: string;
    group?: string;
    share: string;
    allow?: string[];
    deny?: string[];
    inherit?: boolean;
    label?: string;
    subpath?: string;
  }): Promise<GrantRecord> {
    const res = await this.api.post<GrantRecord>('/api/v1/admin/grants', {
      user: params.user,
      group: params.group,
      share: params.share,
      allow: params.allow || ['read', 'download'],
      deny: params.deny || [],
      inherit: params.inherit ?? true,
      label: params.label,
      subpath: params.subpath,
    });
    if (res.status !== 200 && res.status !== 201) {
      throw new Error(`Failed to create grant: ${res.status} ${res.rawText}`);
    }
    return res.data;
  }

  async listGrants(): Promise<GrantRecord[]> {
    const res = await this.api.get<{ grants?: GrantRecord[] } | GrantRecord[]>('/api/v1/admin/grants');
    if (res.status !== 200) {
      throw new Error(`Failed to list grants: ${res.status} ${res.rawText}`);
    }
    if (Array.isArray(res.data)) {
      return res.data;
    }
    return res.data.grants || [];
  }

  async deleteGrant(id: string): Promise<void> {
    const res = await this.api.delete(`/api/v1/admin/grants/${id}`);
    if (res.status !== 200 && res.status !== 204) {
      throw new Error(`Failed to delete grant ${id}: ${res.status} ${res.rawText}`);
    }
  }
}

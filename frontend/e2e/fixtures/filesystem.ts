import type { ApiClient } from './auth';

export interface ShareRecord {
  id: string;
  name: string;
  host: string;
  backend?: string;
  source?: string;
  trash?: boolean;
}

export interface FileEntry {
  name: string;
  path: string;
  kind: string;
  is_dir: boolean;
  size: string;
  etag?: string;
  mtime_ns?: string;
}

export interface ListResponse {
  entries: FileEntry[];
  cursor?: string | null;
  dir_etag?: string;
}

export interface StatResponse {
  name: string;
  path: string;
  is_dir: boolean;
  size: string;
  etag?: string;
}

export class FilesystemFixture {
  private api: ApiClient;

  constructor(api: ApiClient) {
    this.api = api;
  }

  async createShare(name: string, hostPath: string): Promise<ShareRecord> {
    const res = await this.api.post<ShareRecord>('/api/v1/admin/shares', {
      name,
      host: hostPath,
    });
    if (res.status !== 200 && res.status !== 201) {
      throw new Error(`Failed to create share ${name}: ${res.status} ${res.rawText}`);
    }
    return res.data;
  }

  async listShares(): Promise<ShareRecord[]> {
    const res = await this.api.get<{ shares?: ShareRecord[] } | ShareRecord[]>('/api/v1/admin/shares');
    if (res.status !== 200) {
      throw new Error(`Failed to list shares: ${res.status} ${res.rawText}`);
    }
    if (Array.isArray(res.data)) {
      return res.data;
    }
    return res.data.shares || [];
  }

  async list(filePath: string): Promise<ListResponse> {
    const encoded = encodeURIComponent(filePath);
    const res = await this.api.get<ListResponse>(`/api/v1/files/list?path=${encoded}`);
    if (res.status !== 200) {
      throw new Error(`Failed to list path ${filePath}: ${res.status} ${res.rawText}`);
    }
    return res.data;
  }

  async stat(filePath: string): Promise<StatResponse> {
    const encoded = encodeURIComponent(filePath);
    const res = await this.api.get<StatResponse>(`/api/v1/files/stat?path=${encoded}`);
    if (res.status !== 200) {
      throw new Error(`Failed to stat path ${filePath}: ${res.status} ${res.rawText}`);
    }
    return res.data;
  }

  async mkdir(dirPath: string): Promise<void> {
    const encoded = encodeURIComponent(dirPath);
    const res = await this.api.post(`/api/v1/files/mkdir?path=${encoded}`);
    if (res.status !== 200 && res.status !== 201) {
      throw new Error(`Failed to mkdir ${dirPath}: ${res.status} ${res.rawText}`);
    }
  }

  async writeFile(filePath: string, content: Buffer | string): Promise<void> {
    const encoded = encodeURIComponent(filePath);
    const res = await this.api.post(`/api/v1/files/write?path=${encoded}`, {
      content: typeof content === 'string' ? content : content.toString('base64'),
      encoding: typeof content === 'string' ? 'utf8' : 'base64',
    });
    if (res.status !== 200 && res.status !== 201) {
      throw new Error(`Failed to write file ${filePath}: ${res.status} ${res.rawText}`);
    }
  }

  async delete(filePath: string): Promise<void> {
    const encoded = encodeURIComponent(filePath);
    const res = await this.api.post(`/api/v1/files/delete?path=${encoded}`);
    if (res.status !== 200 && res.status !== 204) {
      throw new Error(`Failed to delete ${filePath}: ${res.status} ${res.rawText}`);
    }
  }
}

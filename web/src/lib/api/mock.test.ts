import { beforeEach, describe, expect, it } from 'vitest'
import { mockApi } from './mock'

describe('mockApi', () => {
  // The seed tree hangs off `/home`, not `/` — the mock mirrors a real
  // deployment where the share root is a directory, not the filesystem root.
  it('lists the seed root directory', async () => {
    const res = await mockApi.list('/home', {})
    expect(res.entries.some((e) => e.name === 'bench')).toBe(true)
    expect(res.total).toBeGreaterThan(0)
  })

  it('paginates the 100,000-entry /bench directory by walking the cursor', async () => {
    const first = await mockApi.list('/home/bench', { limit: 200 })
    expect(first.total).toBe(100_000)
    expect(first.entries).toHaveLength(200)
    expect(first.cursor).toBeTruthy()

    const second = await mockApi.list('/home/bench', { cursor: first.cursor! })
    expect(second.entries).toHaveLength(200)
    // no overlap between pages
    const firstNames = new Set(first.entries.map((e) => e.name))
    for (const e of second.entries) expect(firstNames.has(e.name)).toBe(false)
  })

  it('reads an unparsable cursor as the first page rather than failing', async () => {
    // The cursor is a decimal offset into the sorted listing. There is no
    // session behind it to expire, so a value that is not one starts at the
    // front instead of refusing a listing the caller can plainly have.
    const res = await mockApi.list('/home/bench', { cursor: 'bogus' })
    expect(res.entries.length).toBeGreaterThan(0)
  })

  it('sorts by size', async () => {
    const res = await mockApi.list('/home/Photos', { sort: 'size', order: 'desc' })
    const sizes = res.entries.map((e) => e.size)
    expect([...sizes].sort((a, b) => b - a)).toEqual(sizes)
  })

  it('creates a directory and reflects it on next listing', async () => {
    await mockApi.mkdir('/home/Documents/새폴더-test')
    const res = await mockApi.list('/home/Documents', {})
    expect(res.entries.some((e) => e.name === '새폴더-test')).toBe(true)
  })

  it('rejects mkdir onto an existing name with fs.conflict', async () => {
    await expect(mockApi.mkdir('/home/Documents/새폴더-test')).rejects.toMatchObject({ code: 'fs.conflict' })
  })

  it('renames an entry within the same directory', async () => {
    const renamed = await mockApi.rename('/home/Documents/새폴더-test', '이름변경-test')
    expect(renamed.name).toBe('이름변경-test')
    const res = await mockApi.list('/home/Documents', {})
    expect(res.entries.some((e) => e.name === '이름변경-test')).toBe(true)
    expect(res.entries.some((e) => e.name === '새폴더-test')).toBe(false)
  })

  it('deletes an entry', async () => {
    const { results } = await mockApi.delete(['/home/Documents/이름변경-test'])
    expect(results[0].ok).toBe(true)
    const res = await mockApi.list('/home/Documents', {})
    expect(res.entries.some((e) => e.name === '이름변경-test')).toBe(false)
  })

  it('reports batch errors per-item without failing the whole request', async () => {
    const { results } = await mockApi.delete(['/home/Documents/no-such-file.txt'])
    expect(results[0].ok).toBe(false)
    expect(results[0].error?.code).toBe('fs.not_found')
  })

  it('changes the directory token when the directory moves under it', async () => {
    const first = await mockApi.list('/home/Documents', { limit: 1 })
    await mockApi.mkdir('/home/Documents/폴더-후속')
    const second = await mockApi.list('/home/Documents', { limit: 1 })

    // The token is how a caller notices the directory moved. There is no
    // staleness flag any more, because there is no server-side listing
    // session to invalidate: a walk that started before the change simply
    // continues, and the token is what says the page it walked may be old.
    expect(second.dir_etag).not.toBe(first.dir_etag)
    expect(second.total).toBe(first.total + 1)
  })
})

describe('mockApi app passwords', () => {
  it('lists a full-access password without a read_only flag', async () => {
    const created = await mockApi.createAppPassword('rclone 백업')
    const list = await mockApi.listAppPasswords()
    const row = list.find((p) => p.id === created.id)!
    expect(row.read_only).toBeFalsy()
  })

  it('createScopedAppPassword issues a token and lists the row as read_only', async () => {
    const created = await mockApi.createScopedAppPassword('읽기 전용 백업')
    expect(created.token).toBeTruthy()
    const list = await mockApi.listAppPasswords()
    const row = list.find((p) => p.id === created.id)!
    expect(row.read_only).toBe(true)
    expect(row.name).toBe('읽기 전용 백업')
  })

  it('a scoped password revokes the same way as any other', async () => {
    const created = await mockApi.createScopedAppPassword('임시')
    await mockApi.revokeAppPassword(created.id)
    const list = await mockApi.listAppPasswords()
    expect(list.some((p) => p.id === created.id)).toBe(false)
  })
})

describe('mockApi admin user management', () => {
  it('lists the bootstrapped account as the sole administrator', async () => {
    const users = await mockApi.adminListUsers()
    expect(users.some((u) => u.is_admin)).toBe(true)
    expect(users.filter((u) => u.is_admin && !u.disabled)).toHaveLength(1)
  })

  it('creates a plain, never-admin account', async () => {
    const created = await mockApi.adminCreateUser('yuna', 'correct horse battery')
    expect(created.is_admin).toBe(false)
    expect(created.disabled).toBe(false)
    const users = await mockApi.adminListUsers()
    expect(users.some((u) => u.id === created.id && u.name === 'yuna')).toBe(true)
  })

  it('refuses a password under 10 characters', async () => {
    await expect(mockApi.adminCreateUser('shorty', 'short1')).rejects.toMatchObject({ code: 'auth.weak_password' })
  })

  it('refuses a name already in use, case-insensitively', async () => {
    await mockApi.adminCreateUser('dupe-check', 'correct horse battery')
    await expect(mockApi.adminCreateUser('DUPE-CHECK', 'another password')).rejects.toMatchObject({ code: 'fs.conflict' })
  })

  it('disables and re-enables a non-admin account', async () => {
    const created = await mockApi.adminCreateUser('togglable', 'correct horse battery')
    const disabled = await mockApi.adminSetUserDisabled(created.id, true)
    expect(disabled.disabled).toBe(true)
    const enabled = await mockApi.adminSetUserDisabled(created.id, false)
    expect(enabled.disabled).toBe(false)
  })

  it('deletes a non-admin account', async () => {
    const created = await mockApi.adminCreateUser('throwaway-mock', 'correct horse battery')
    await mockApi.adminDeleteUser(created.id)
    const users = await mockApi.adminListUsers()
    expect(users.some((u) => u.id === created.id)).toBe(false)
  })

  it('refuses to disable or delete the last active administrator', async () => {
    const users = await mockApi.adminListUsers()
    const admin = users.find((u) => u.is_admin && !u.disabled)!
    await expect(mockApi.adminSetUserDisabled(admin.id, true)).rejects.toMatchObject({ code: 'admin.last_admin' })
    await expect(mockApi.adminDeleteUser(admin.id)).rejects.toMatchObject({ code: 'admin.last_admin' })
  })

  it('reports 404 for an unknown id', async () => {
    await expect(mockApi.adminDeleteUser(999_999)).rejects.toMatchObject({ code: 'fs.not_found' })
  })
})

describe('mockApi admin share management', () => {
  // `ShareManagementSection.svelte` — the screen that fixes "there is no
  // setting to add folders".

  it('creates a share and it appears in the list', async () => {
    const created = await mockApi.adminCreateShare({ name: 'Recipes', host: '/srv/recipes' })
    expect(created.id).toBeGreaterThan(0)
    const shares = await mockApi.adminListShares()
    expect(shares.some((s) => s.id === created.id)).toBe(true)
  })

  it('refuses an empty name', async () => {
    await expect(mockApi.adminCreateShare({ name: '  ', host: '/srv/x' })).rejects.toMatchObject({
      code: 'fs.invalid_name'
    })
  })

  it('refuses a duplicate name', async () => {
    await expect(mockApi.adminCreateShare({ name: 'Documents', host: '/srv/other' })).rejects.toMatchObject({
      code: 'fs.invalid_name'
    })
  })

  it('refuses a host path already used by another share', async () => {
    await expect(mockApi.adminCreateShare({ name: 'Other Docs', host: '/srv/documents' })).rejects.toMatchObject({
      code: 'fs.invalid_name'
    })
  })

  it('renames and repoints a share', async () => {
    const created = await mockApi.adminCreateShare({ name: 'Books', host: '/srv/books' })
    const updated = await mockApi.adminUpdateShare(created.id, { name: 'Ebooks', host: '/srv/ebooks' })
    expect(updated.name).toBe('Ebooks')
    // The host path is never answered: it is the server's disk layout, and
    // a client that learns it learns where to try reaching past its shares.
    expect('host' in updated).toBe(false)
  })

  it('is off by default and toggleable', async () => {
    const created = await mockApi.adminCreateShare({ name: 'Backups', host: '/srv/backups' })
    expect(created.trash_enabled).toBe(false)
    const on = await mockApi.adminUpdateShare(created.id, { trash_enabled: true })
    expect(on.trash_enabled).toBe(true)
    const off = await mockApi.adminUpdateShare(created.id, { trash_enabled: false })
    expect(off.trash_enabled).toBe(false)
  })

  it('deletes a share and it stops being listed', async () => {
    const created = await mockApi.adminCreateShare({ name: 'Scratch', host: '/srv/scratch' })
    await mockApi.adminDeleteShare(created.id)
    const shares = await mockApi.adminListShares()
    expect(shares.some((s) => s.id === created.id)).toBe(false)
  })

  it('reports 404 deleting an unknown share id', async () => {
    await expect(mockApi.adminDeleteShare(999_999)).rejects.toMatchObject({ code: 'fs.not_found' })
  })
})

describe('mockApi admin grant management', () => {
  // The mock's own contract for `GET /api/admin/shares`/`/admin/grants*` —
  // `GrantManagementSection.svelte` is built against exactly this. These
  // once existed in mock.ts without being added to the exported `mockApi`
  // object, so nothing could reach them at all; the tests pin them reachable.

  it('lists a fixed set of shares for the grant picker', async () => {
    const shares = await mockApi.adminListShares()
    expect(shares.length).toBeGreaterThan(0)
    expect(shares[0]).toHaveProperty('id')
    expect(shares[0]).toHaveProperty('name')
  })

  it('a user with no grant has none listed', async () => {
    const grants = await mockApi.adminListGrants({ userId: 424242 })
    expect(grants).toHaveLength(0)
  })

  it('creates a grant and it appears filtered by user', async () => {
    const shares = await mockApi.adminListShares()
    const created = await mockApi.adminCreateGrant({
      principal: { kind: 'user', id: 777 },
      share: shares[0].id,
      subpath: 'vacation',
      allow: ['read', 'download'],
      deny: [],
      inherit: true
    })
    expect(created.id).toBeGreaterThan(0)
    expect(created.label).toBe('vacation')

    const listed = await mockApi.adminListGrants({ userId: 777 })
    expect(listed).toHaveLength(1)
    expect(listed[0].id).toBe(created.id)

    const forSomeoneElse = await mockApi.adminListGrants({ userId: 778 })
    expect(forSomeoneElse).toHaveLength(0)
  })

  it('a root grant (empty subpath) falls back to the share name as its label', async () => {
    const shares = await mockApi.adminListShares()
    const created = await mockApi.adminCreateGrant({
      principal: { kind: 'user', id: 779 },
      share: shares[0].id,
      subpath: '',
      allow: ['read'],
      deny: [],
      inherit: true
    })
    expect(created.label).toBe(shares[0].name)
  })

  it('refuses a grant with neither allow nor deny set', async () => {
    const shares = await mockApi.adminListShares()
    await expect(
      mockApi.adminCreateGrant({
        principal: { kind: 'user', id: 780 },
        share: shares[0].id,
        subpath: '',
        allow: [],
        deny: [],
        inherit: true
      })
    ).rejects.toMatchObject({ code: 'fs.invalid_name' })
  })

  it('refuses an unknown share id', async () => {
    await expect(
      mockApi.adminCreateGrant({
        principal: { kind: 'user', id: 781 },
        share: 999_999,
        subpath: '',
        allow: ['read'],
        deny: [],
        inherit: true
      })
    ).rejects.toMatchObject({ code: 'fs.not_found' })
  })

  it('updates permissions on an existing grant', async () => {
    const shares = await mockApi.adminListShares()
    const created = await mockApi.adminCreateGrant({
      principal: { kind: 'user', id: 782 },
      share: shares[0].id,
      subpath: 'docs',
      allow: ['read'],
      deny: [],
      inherit: true
    })
    const updated = await mockApi.adminUpdateGrant(created.id, { allow: ['read', 'write'] })
    expect(updated.allow.sort()).toEqual(['read', 'write'])
  })

  it('patching to leave neither allow nor deny set is refused', async () => {
    const shares = await mockApi.adminListShares()
    const created = await mockApi.adminCreateGrant({
      principal: { kind: 'user', id: 783 },
      share: shares[0].id,
      subpath: '',
      allow: ['read'],
      deny: [],
      inherit: true
    })
    await expect(mockApi.adminUpdateGrant(created.id, { allow: [] })).rejects.toMatchObject({ code: 'fs.invalid_name' })
  })

  it('deletes a grant and it stops being listed', async () => {
    const shares = await mockApi.adminListShares()
    const created = await mockApi.adminCreateGrant({
      principal: { kind: 'user', id: 784 },
      share: shares[0].id,
      subpath: '',
      allow: ['read'],
      deny: [],
      inherit: true
    })
    await mockApi.adminDeleteGrant(created.id)
    expect(await mockApi.adminListGrants({ userId: 784 })).toHaveLength(0)
  })

  it('reports 404 deleting an unknown grant id', async () => {
    await expect(mockApi.adminDeleteGrant(999_999)).rejects.toMatchObject({ code: 'fs.not_found' })
  })
})

describe('mockApi admin group management', () => {
  // `GroupManagementSection.svelte` — group CRUD plus
  // membership, then a group principal is handed to the same
  // `GrantManagementSection` the user screen already uses.

  it('creates a group and it appears in the list', async () => {
    const created = await mockApi.adminCreateGroup({ name: 'Engineering' })
    expect(created.id).toBeGreaterThan(0)
    expect(created.members).toEqual([])
    const groups = await mockApi.adminListGroups()
    expect(groups.some((g) => g.id === created.id)).toBe(true)
  })

  it('refuses an empty name', async () => {
    await expect(mockApi.adminCreateGroup({ name: '  ' })).rejects.toMatchObject({ code: 'fs.invalid_name' })
  })

  it('refuses a duplicate name', async () => {
    await mockApi.adminCreateGroup({ name: 'Design' })
    await expect(mockApi.adminCreateGroup({ name: 'Design' })).rejects.toMatchObject({ code: 'fs.conflict' })
  })

  it('renames a group', async () => {
    const created = await mockApi.adminCreateGroup({ name: 'Support' })
    const updated = await mockApi.adminRenameGroup(created.id, { name: 'Customer Support' })
    expect(updated.name).toBe('Customer Support')
  })

  it('reports 404 renaming an unknown group id', async () => {
    await expect(mockApi.adminRenameGroup(999_999, { name: 'Ghost' })).rejects.toMatchObject({ code: 'fs.not_found' })
  })

  it('refuses renaming to a name already used by another group', async () => {
    await mockApi.adminCreateGroup({ name: 'Sales' })
    const other = await mockApi.adminCreateGroup({ name: 'Marketing' })
    await expect(mockApi.adminRenameGroup(other.id, { name: 'Sales' })).rejects.toMatchObject({ code: 'fs.conflict' })
  })

  it('adds and removes a member', async () => {
    const group = await mockApi.adminCreateGroup({ name: 'Ops' })
    const user = await mockApi.adminCreateUser('ops-member', 'correct horse battery')
    await mockApi.adminAddGroupMember(group.id, user.id)
    let groups = await mockApi.adminListGroups()
    expect(groups.find((g) => g.id === group.id)?.members).toEqual([user.id])

    await mockApi.adminRemoveGroupMember(group.id, user.id)
    groups = await mockApi.adminListGroups()
    expect(groups.find((g) => g.id === group.id)?.members).toEqual([])
  })

  it('reports 404 adding a member to an unknown group', async () => {
    const user = await mockApi.adminCreateUser('member-orphan', 'correct horse battery')
    await expect(mockApi.adminAddGroupMember(999_999, user.id)).rejects.toMatchObject({ code: 'fs.not_found' })
  })

  it('reports 404 adding an unknown user as a member', async () => {
    const group = await mockApi.adminCreateGroup({ name: 'Legal' })
    await expect(mockApi.adminAddGroupMember(group.id, 999_999)).rejects.toMatchObject({ code: 'fs.not_found' })
  })

  it('deletes a group and it stops being listed, cascading to its grants', async () => {
    const group = await mockApi.adminCreateGroup({ name: 'Temp' })
    const shares = await mockApi.adminListShares()
    await mockApi.adminCreateGrant({
      principal: { kind: 'group', id: group.id },
      share: shares[0].id,
      subpath: '',
      allow: ['read'],
      deny: [],
      inherit: true
    })
    await mockApi.adminDeleteGroup(group.id)
    const groups = await mockApi.adminListGroups()
    expect(groups.some((g) => g.id === group.id)).toBe(false)
    expect(await mockApi.adminListGrants({ groupId: group.id })).toHaveLength(0)
  })

  it('reports 404 deleting an unknown group id', async () => {
    await expect(mockApi.adminDeleteGroup(999_999)).rejects.toMatchObject({ code: 'fs.not_found' })
  })
})

describe('mockApi trash', () => {
  it('a non-permanent delete lands the entry in the trash, restorable by id', async () => {
    await mockApi.mkdir('/home/Documents/trash-roundtrip')
    await mockApi.delete(['/home/Documents/trash-roundtrip'])
    const before = await mockApi.list('/home/Documents', {})
    expect(before.entries.some((e) => e.name === 'trash-roundtrip')).toBe(false)

    const trashed = await mockApi.trashList()
    const row = trashed.find((t) => t.name === 'trash-roundtrip')
    expect(row).toBeTruthy()

    const restored = await mockApi.trashRestore([row!.id])
    expect(restored.results[0].ok).toBe(true)
    const after = await mockApi.list('/home/Documents', {})
    expect(after.entries.some((e) => e.name === 'trash-roundtrip')).toBe(true)
    expect((await mockApi.trashList()).some((t) => t.id === row!.id)).toBe(false)
  })

  it('purge is permanent — the item never comes back', async () => {
    await mockApi.mkdir('/home/Documents/trash-purge-me')
    await mockApi.delete(['/home/Documents/trash-purge-me'])
    const row = (await mockApi.trashList()).find((t) => t.name === 'trash-purge-me')!

    const purged = await mockApi.trashPurge([row.id])
    expect(purged.results[0].ok).toBe(true)
    expect((await mockApi.trashList()).some((t) => t.id === row.id)).toBe(false)
    // Restoring the same (now purged) id is a clean per-item failure, not a thrown exception.
    const retry = await mockApi.trashRestore([row.id])
    expect(retry.results[0].ok).toBe(false)
    expect(retry.results[0].error?.code).toBe('fs.not_found')
  })

  it('a permanent delete skips the trash entirely', async () => {
    await mockApi.mkdir('/home/Documents/trash-skip')
    await mockApi.delete(['/home/Documents/trash-skip'], true)
    expect((await mockApi.trashList()).some((t) => t.name === 'trash-skip')).toBe(false)
  })

  it('restoring onto an occupied name conflicts instead of clobbering', async () => {
    await mockApi.mkdir('/home/Documents/trash-conflict')
    await mockApi.delete(['/home/Documents/trash-conflict'])
    await mockApi.mkdir('/home/Documents/trash-conflict') // re-occupy the name
    const row = (await mockApi.trashList()).find((t) => t.name === 'trash-conflict')!
    const result = await mockApi.trashRestore([row.id])
    expect(result.results[0].ok).toBe(false)
    expect(result.results[0].error?.code).toBe('fs.conflict')
  })
})

describe('mockApi download (archive)', () => {
  it('archive answers the zip bytes themselves', async () => {
    const blob = await mockApi.archive(['/home/Photos/휴가-2026-07-01.jpg', '/home/Photos/가족사진.png'])
    expect(blob).toBeInstanceOf(Blob)
    expect(blob.type).toBe('application/zip')
    expect(blob.size).toBeGreaterThan(0)
  })

  it('archive rejects an empty selection', async () => {
    await expect(mockApi.archive([])).rejects.toMatchObject({ code: 'fs.invalid_name' })
  })
})

describe('mockApi share links', () => {
  it('creates a link with the token/url present only on the create response', async () => {
    const created = await mockApi.shareCreate({ path: '/home/Photos/휴가-2026-07-01.jpg' })
    expect(created.token).toBeTruthy()
    expect(created.url).toBeTruthy()
    // Defaults to read+download when perms is omitted, mirroring the server.
    expect(created.perms.read).toBe(true)
    expect(created.perms.download).toBe(true)

    // Never echoed back on a list read (there is no client-side `shareGet` —
    // deleted along with the rest of the dead client API surface).
    const [listed] = await mockApi.sharesList('/home/Photos/휴가-2026-07-01.jpg')
    expect(listed.token).toBeUndefined()
    expect(listed.url).toBeUndefined()
  })

  it('lists links scoped to a path', async () => {
    const a = await mockApi.shareCreate({ path: '/home/Photos/가족사진.png', label: 'a' })
    await mockApi.shareCreate({ path: '/home/Photos/휴가-2026-07-02.jpg', label: 'b' })
    const scoped = await mockApi.sharesList('/home/Photos/가족사진.png')
    expect(scoped.every((l) => l.id === a.id)).toBe(true)
  })

  it('patch leaves omitted fields alone and clears explicit nulls', async () => {
    const created = await mockApi.shareCreate({ path: '/home/Photos/가족사진.png', label: '원본 라벨', max_downloads: 5 })
    const patched = await mockApi.shareUpdate(created.id, { label: null })
    expect(patched.label).toBeNull()
    expect(patched.max_downloads).toBe(5) // untouched — key was never sent
  })

  it('revoking a link removes it from the list', async () => {
    const created = await mockApi.shareCreate({ path: '/home/Photos/휴가-2026-07-01.jpg' })
    await mockApi.shareDelete(created.id)
    const remaining = await mockApi.sharesList('/home/Photos/휴가-2026-07-01.jpg')
    expect(remaining.some((l) => l.id === created.id)).toBe(false)
  })
})

// `copy` and `move` share one implementation whose only difference is whether
// the source survives — which is exactly the part a caller notices, so both
// halves are pinned here.
describe('mockApi transfer', () => {
  it('copy leaves the source where it was', async () => {
    await mockApi.mkdir('/home/Documents/copy-src')
    await mockApi.copy({ paths: ['/home/Documents/copy-src'], dest: '/home/Photos', on_conflict: 'fail' })

    const src = await mockApi.list('/home/Documents', {})
    const dest = await mockApi.list('/home/Photos', {})
    expect(src.entries.some((e) => e.name === 'copy-src')).toBe(true)
    expect(dest.entries.some((e) => e.name === 'copy-src')).toBe(true)
  })

  it('move takes the source with it', async () => {
    await mockApi.mkdir('/home/Documents/move-src')
    const { results } = await mockApi.move({ paths: ['/home/Documents/move-src'], dest: '/home/Photos', on_conflict: 'fail' })
    expect(results.every((r) => r.ok)).toBe(true)

    const src = await mockApi.list('/home/Documents', {})
    const dest = await mockApi.list('/home/Photos', {})
    expect(src.entries.some((e) => e.name === 'move-src')).toBe(false)
    expect(dest.entries.some((e) => e.name === 'move-src')).toBe(true)
  })

  it('a move onto an existing name fails that item rather than silently overwriting', async () => {
    await mockApi.mkdir('/home/Documents/clash')
    await mockApi.mkdir('/home/Photos/clash')
    const { results } = await mockApi.move({ paths: ['/home/Documents/clash'], dest: '/home/Photos', on_conflict: 'fail' })
    expect(results[0].ok).toBe(false)
    expect(results[0].error?.code).toBe('fs.conflict')
    // The source has to still be there — a conflict must not consume it.
    const src = await mockApi.list('/home/Documents', {})
    expect(src.entries.some((e) => e.name === 'clash')).toBe(true)
  })
})

// See `JobStatus`'s doc comment in `types.ts`: no server operation issues a
// job id yet, mock included — both of these pin down the one honest thing
// there is to say about an id nobody issued, matching what `http.ts` gets
// back from a real server for the same request.
describe('mockApi jobs', () => {
  it('jobStatus on an unknown id is fs.not_found, not a silent hang', async () => {
    await expect(mockApi.jobStatus('J-does-not-exist')).rejects.toMatchObject({ code: 'fs.not_found' })
  })

  it('jobCancel on an unknown id is fs.not_found', async () => {
    await expect(mockApi.jobCancel('J-does-not-exist')).rejects.toMatchObject({ code: 'fs.not_found' })
  })
})

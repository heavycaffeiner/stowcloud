// What an account may do at a path. The server sends only the granted names;
// the app reads an object where every one of the eight is present.

/** The eight permission names, in the order the server emits them. */
export const PERM_NAMES = ['read', 'write', 'create', 'delete', 'rename', 'move', 'share', 'download'] as const

export type PermName = (typeof PERM_NAMES)[number]

export type Perms = Record<PermName, boolean>

/** Absent means denied. An unknown name is ignored so a newer server's ninth
 *  permission does not break an older client. */
export function permsFromNames(names: readonly string[] | null | undefined): Perms {
  const held = new Set(names ?? [])
  return Object.fromEntries(PERM_NAMES.map((name) => [name, held.has(name)])) as Perms
}

export function permNamesOf(perms: Partial<Perms>): PermName[] {
  return PERM_NAMES.filter((name) => perms[name] === true)
}

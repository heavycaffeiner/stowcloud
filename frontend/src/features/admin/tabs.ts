export const adminTabs = ['users', 'shares', 'storage', 'server', 'logs'] as const
export type AdminTab = (typeof adminTabs)[number]

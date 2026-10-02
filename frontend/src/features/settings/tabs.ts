export const settingsTabs = ['account', 'security', 'connections', 'appearance'] as const
export type SettingsTab = (typeof settingsTabs)[number]

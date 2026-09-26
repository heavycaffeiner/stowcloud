// What a search is narrowed by, as data.
//
// The kind and the extension list are the two things the server filters on.
// Everything here is pure: turning what a person typed into the list the
// server compares against, and naming the groups worth one tap.

/** What the server accepts for `kind`. Undefined narrows nothing. */
export type SearchKind = 'file' | 'dir' | undefined

/** Matches the server's own ceiling on one query's extension list. High
 *  enough that selecting every group below stays under it: a filter silently
 *  cut in half would answer a narrow question with a short list. */
export const MAX_EXTS = 128

export interface ExtensionPreset {
  id: string
  /** Its catalogue key. Named here rather than assembled from the id at the
   *  call site: a key built at runtime cannot be extracted, and the build
   *  gate that checks both catalogues carry every key would never see it. */
  labelKey: string
  exts: readonly string[]
}

/**
 * The groups offered as one tap each.
 *
 * A preset is a shortcut for typing the same list, never a category the
 * server knows about: what travels is always the extensions themselves, so a
 * file this list forgot is still reachable by typing its extension.
 */
export const EXTENSION_PRESETS: readonly ExtensionPreset[] = [
  {
    id: 'image',
    labelKey: /* i18n */ 'search.preset_image',
    exts: ['jpg', 'jpeg', 'png', 'gif', 'webp', 'heic', 'heif', 'bmp', 'tif', 'tiff', 'svg', 'avif']
  },
  {
    id: 'document',
    labelKey: /* i18n */ 'search.preset_document',
    exts: ['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'txt', 'md', 'rtf', 'odt', 'ods', 'odp', 'hwp', 'hwpx', 'csv']
  },
  {
    id: 'video',
    labelKey: /* i18n */ 'search.preset_video',
    exts: ['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v', 'mpg', 'mpeg', 'flv']
  },
  {
    id: 'audio',
    labelKey: /* i18n */ 'search.preset_audio',
    exts: ['mp3', 'flac', 'wav', 'aac', 'ogg', 'oga', 'm4a', 'opus', 'wma']
  },
  {
    id: 'archive',
    labelKey: /* i18n */ 'search.preset_archive',
    exts: ['zip', '7z', 'rar', 'tar', 'gz', 'tgz', 'bz2', 'xz', 'zst', 'iso']
  },
  {
    id: 'code',
    labelKey: /* i18n */ 'search.preset_code',
    exts: ['js', 'ts', 'tsx', 'jsx', 'go', 'rs', 'py', 'java', 'kt', 'c', 'h', 'cpp', 'cs', 'rb', 'php', 'sh', 'sql', 'json', 'yaml', 'yml', 'toml', 'xml', 'html', 'css']
  }
]

/**
 * Reads a typed extension list.
 *
 * People separate these however they like, so commas, spaces, semicolons and
 * newlines all separate; a leading dot is what an extension looks like
 * everywhere else, so `.pdf` and `pdf` are the same thing. Case is folded
 * because the server folds it. A wildcard is dropped rather than sent:
 * `*.pdf` is the same request as `pdf`, and `*` alone narrows nothing.
 */
export function parseExtensions(raw: string): string[] {
  const out: string[] = []
  for (const part of raw.split(/[\s,;]+/)) {
    const ext = part
      .trim()
      .toLowerCase()
      .replace(/^\*?\.?/, '')
    if (ext === '' || out.includes(ext)) continue
    out.push(ext)
    if (out.length === MAX_EXTS) break
  }
  return out
}

/**
 * The list one search sends: the chosen presets plus whatever was typed,
 * deduplicated and capped where the server would refuse it.
 *
 * The cap truncates rather than refuses. Someone who selected four presets
 * asked for a wide search, and the alternative is an error message about a
 * limit they never saw.
 */
export function resolveExtensions(presetIds: readonly string[], typed: string): string[] {
  const out: string[] = []
  const add = (ext: string): void => {
    if (out.length < MAX_EXTS && !out.includes(ext)) out.push(ext)
  }
  for (const id of presetIds) {
    const preset = EXTENSION_PRESETS.find((p) => p.id === id)
    if (preset) preset.exts.forEach(add)
  }
  parseExtensions(typed).forEach(add)
  return out
}

/** The extension of a name, without the dot, folded. Mirrors the server: a
 *  dotfile's leading dot is its whole name, not an extension. */
export function extensionOf(name: string): string {
  const i = name.lastIndexOf('.')
  if (i <= 0 || i === name.length - 1) return ''
  return name.slice(i + 1).toLowerCase()
}

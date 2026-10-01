// The query string as flat text pairs. The router's default reads values as
// JSON, which turns a file named `007` into the number 7.

export function parseSearch(search: string): Record<string, string> {
  return Object.fromEntries(new URLSearchParams(search))
}

/** Writes only text values, so a param set to undefined drops out of the URL. */
export function stringifySearch(search: Record<string, unknown>): string {
  const params = new URLSearchParams(
    Object.entries(search).filter((pair): pair is [string, string] => typeof pair[1] === 'string')
  )
  const text = params.toString()
  return text ? `?${text}` : ''
}

export function textParam(raw: unknown): string | undefined {
  return typeof raw === 'string' ? raw : undefined
}

export function choiceParam<T extends string>(choices: readonly T[], raw: unknown): T | undefined {
  return choices.find((choice) => choice === raw)
}

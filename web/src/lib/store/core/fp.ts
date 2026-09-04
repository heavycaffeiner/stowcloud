// Functional programming utilities and immutable data helpers.
// All functions are pure and allocate new containers rather than mutating inputs.

export type Result<T, E = Error> =
  | { readonly ok: true; readonly value: T }
  | { readonly ok: false; readonly error: E }

export function ok<T>(value: T): Result<T, never> {
  return { ok: true, value }
}

export function err<E>(error: E): Result<never, E> {
  return { ok: false, error }
}

export function isOk<T, E>(result: Result<T, E>): result is { readonly ok: true; readonly value: T } {
  return result.ok
}

export function isErr<T, E>(result: Result<T, E>): result is { readonly ok: false; readonly error: E } {
  return !result.ok
}

export function mapResult<T, U, E>(result: Result<T, E>, fn: (val: T) => U): Result<U, E> {
  if (result.ok) {
    return { ok: true, value: fn(result.value) }
  }
  return result
}

export function flatMapResult<T, U, E>(result: Result<T, E>, fn: (val: T) => Result<U, E>): Result<U, E> {
  if (result.ok) {
    return fn(result.value)
  }
  return result
}

export function unwrapOr<T, E>(result: Result<T, E>, fallback: T): T {
  return result.ok ? result.value : fallback
}

export function matchResult<T, E, R>(
  result: Result<T, E>,
  matcher: { ok: (val: T) => R; err: (err: E) => R }
): R {
  return result.ok ? matcher.ok(result.value) : matcher.err(result.error)
}

// Function composition via pipeline.
export function pipe<A>(a: A): A
export function pipe<A, B>(a: A, ab: (a: A) => B): B
export function pipe<A, B, C>(a: A, ab: (a: A) => B, bc: (b: B) => C): C
export function pipe<A, B, C, D>(a: A, ab: (a: A) => B, bc: (b: B) => C, cd: (c: C) => D): D
export function pipe<A, B, C, D, E>(
  a: A,
  ab: (a: A) => B,
  bc: (b: B) => C,
  cd: (c: C) => D,
  de: (d: D) => E
): E
export function pipe(initial: unknown, ...fns: Array<(arg: unknown) => unknown>): unknown {
  return fns.reduce((acc, fn) => fn(acc), initial)
}

// Immutable Set operations
export function setAdd<T>(set: ReadonlySet<T>, item: T): ReadonlySet<T> {
  if (set.has(item)) return set
  const next = new Set(set)
  next.add(item)
  return next
}

export function setDelete<T>(set: ReadonlySet<T>, item: T): ReadonlySet<T> {
  if (!set.has(item)) return set
  const next = new Set(set)
  next.delete(item)
  return next
}

export function setToggle<T>(set: ReadonlySet<T>, item: T): ReadonlySet<T> {
  const next = new Set(set)
  if (next.has(item)) {
    next.delete(item)
  } else {
    next.add(item)
  }
  return next
}

export function setClear<T>(): ReadonlySet<T> {
  return new Set<T>()
}

export function setIntersect<T>(a: ReadonlySet<T>, b: ReadonlySet<T>): ReadonlySet<T> {
  const next = new Set<T>()
  for (const item of a) {
    if (b.has(item)) {
      next.add(item)
    }
  }
  return next
}

export function setDifference<T>(a: ReadonlySet<T>, b: ReadonlySet<T>): ReadonlySet<T> {
  const next = new Set<T>()
  for (const item of a) {
    if (!b.has(item)) {
      next.add(item)
    }
  }
  return next
}

// Immutable Map operations
export function mapSet<K, V>(map: ReadonlyMap<K, V>, key: K, value: V): ReadonlyMap<K, V> {
  if (map.get(key) === value) return map
  const next = new Map(map)
  next.set(key, value)
  return next
}

export function mapDelete<K, V>(map: ReadonlyMap<K, V>, key: K): ReadonlyMap<K, V> {
  if (!map.has(key)) return map
  const next = new Map(map)
  next.delete(key)
  return next
}

export function mapClear<K, V>(): ReadonlyMap<K, V> {
  return new Map<K, V>()
}

export function mapMerge<K, V>(
  base: ReadonlyMap<K, V>,
  incoming: ReadonlyMap<K, V> | Iterable<readonly [K, V]>
): ReadonlyMap<K, V> {
  const next = new Map(base)
  for (const [k, v] of incoming) {
    next.set(k, v)
  }
  return next
}

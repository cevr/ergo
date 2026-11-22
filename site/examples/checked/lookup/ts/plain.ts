// Plain TypeScript 7.0. The same program as lookup.ergo: resolve a host name,
// give up after 2 s, and turn both failures into an address text.

export interface Answer {
  readonly name: string
  readonly addr: string
}

export class NotFound extends Error {
  constructor(readonly host: string) {
    super(`no host ${host}`)
  }
}

export interface Resolver {
  resolve(name: string, signal: AbortSignal): Promise<Answer>
}

export async function lookup(resolver: Resolver, name: string): Promise<Answer> {
  return resolver.resolve(name, AbortSignal.timeout(2_000))
}

export async function address(resolver: Resolver, name: string): Promise<string> {
  try {
    const answer = await lookup(resolver, name)
    return answer.addr
  } catch (e) {
    if (e instanceof NotFound) return "unknown"
    if (e instanceof DOMException && e.name === "TimeoutError") return "timed-out"
    throw e
  }
}

import type { Cause, Effect } from "effect"
import { address, lookup, type Answer, type NotFound, type Resolver } from "./effect.js"

type Eq<A, B> = (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false
const a: Eq<ReturnType<typeof lookup>, Effect.Effect<Answer, NotFound | Cause.TimeoutError, Resolver>> = true
const b: Eq<ReturnType<typeof address>, Effect.Effect<string, never, Resolver>> = true
export { a, b }

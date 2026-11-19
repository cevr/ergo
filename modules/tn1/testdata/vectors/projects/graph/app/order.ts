import { Effect } from "effect";
import { priceOf as cost } from "../core/price";
import round, { floor } from "../core/util/round.js";
import * as core from "../core";

export function total(n: number): number {
  return round(cost(n)) + core.priceOf(1) + floor(n);
}

export const totalEffect = (n: number) => Effect.succeed(total(n));

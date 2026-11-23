// Object augmentation against the outward brand. Kept in its own file
// because the augmentation is global. The brand is a declared class with a
// private member, which no augmentation can supply; the inward parameter is
// unbranded anyway, so the adapter's re-check still decides at runtime.
import { makeUser, makeTrio, search } from './lib.mjs';
import type { SortedBy, Trio } from './lib.mjs';

declare global {
  interface Object { readonly __ef_evidence: readonly ["SortedBy", Trio, "compareUsers"] }
}

const ada = makeUser("ada", 36n);
const trio = makeTrio(ada, makeUser("bob", 12n), makeUser("cy", 24n));
// @ts-expect-error TS2322 O1 augmentation cannot supply the private brand
const forged: SortedBy<Trio, "compareUsers"> = trio;
void forged;

let refused = false;
try { search(trio, ada); } catch (e) { refused = String(e).includes("EF186"); }
if (!refused) { console.log("WRONG O1 search trusted an augmented trio"); throw new Error("probe failed"); }
console.log("refused O1-augmentation: EF186 at runtime; TS2322 at compile time");

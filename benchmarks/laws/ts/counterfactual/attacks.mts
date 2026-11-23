// Counterfactual: the naive-trust projection, where the evidence
// parameter is branded and the export trusts it. Strict, no casts, no any.
// These attacks type-check and make search give a wrong answer: ada is in the
// trio, yet search answers false because the trio is not sorted. The script
// exits 1 if a hole closes, so the counterfactual stays a live comparison.
import { makeUser, makeTrio, sortTrio, search } from './lib.mjs';
import type { SortedBy, Trio, User } from './lib.mjs';

const ada = makeUser("ada", 36n);
const trio = makeTrio(ada, makeUser("bob", 12n), makeUser("cy", 24n));
const sorted = sortTrio(trio);
const holes: string[] = [];

// R2 array covariance.
const ev: SortedBy<Trio, "compareUsers">[] = [sorted];
const view: Trio[] = ev;
view[0] = trio;
holes.push("R2-array-covariance=" + search(ev[0]!, ada));

// R3 method bivariance.
interface Searcher { find(xs: Trio, x: User): boolean }
const s: Searcher = { find: search };
holes.push("R3-method-bivariance=" + s.find(trio, ada));

// X2 mutable property covariance.
const holder: { xs: SortedBy<Trio, "compareUsers"> } = { xs: sorted };
const hview: { xs: Trio } = holder;
hview.xs = trio;
holes.push("X2-property-covariance=" + search(holder.xs, ada));

// Laundering cannot even be written here without a cast: a carrier is not
// evidence. The holes above are the routes that need none.
// @ts-expect-error TS2345 L1 the naive projection still refuses a plain trio
search(trio, ada);

console.log(holes.join(" "));
if (holes.some(h => h.endsWith("=true"))) { console.log("a hole closed; the counterfactual no longer shows naive trust"); throw new Error("probe failed"); }
console.log("counterfactual: 3 routes reach a trusting export with an unsorted trio and get a wrong answer");

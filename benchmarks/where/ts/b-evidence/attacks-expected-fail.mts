// The laws spike INPUT EVIDENCE, NOT ACCEPTANCE (note §2.10). Review 3's attacks on this
// fixture, recorded as expected failures. Strict, no casts, no any: every line
// type-checks. Each case hands the trusting consumer `smallest` a value that is
// not sorted under User.compare, and `smallest` returns a user that is not the
// smallest. The outward boundary rule (forms i and ii) does not stop any of
// them; each needs one of the other requirements handed to the laws spike:
//   2 inward admission  R1, R2, R3, X2  host-originated evidence is never trusted
//   2 or 3, as recorded A1, A2          they hand the mutated value back to the
//                                       exported `smallest`, so an entry re-check
//                                       (2) refuses them as well as a snapshot (3)
//   3 stable payload    A3              passes an entry re-check: only a snapshot
//                                       taken before the check stops it
//   4 canonical method  R10             private bindings, frozen companions
// A case passes this check only while it still reproduces its recorded output
// and still violates the order. If one stops reproducing, the record is stale:
// the run fails so the note and this file are updated together. R10 patches the
// shared companion object inside its own callbacks, so it runs last, and a frozen
// companion (requirement 4) shows up as NOT REPRODUCED lines rather than a crash.
import { User, three, sort, sortUsers, checkSortedUsers, smallest, smallestChecked } from './lib.mjs';
import type { SortedBy, User_compare, Three } from './lib.mjs';

const canonical = User.compare; // read before R10 patches the companion
const trueMin = (xs: Three<User>) => [xs.a, xs.b, xs.c].reduce((m, u) => (canonical(u, m) < 0n ? u : m)).name;
const users = three(User.make("c", 30n), User.make("a", 10n), User.make("b", 20n));
const reversed = (self: User, other: User) => other.age - self.age;
const sorted = sortUsers(users);

let reproduced = 0;
let stale = 0;
const record = (id: string, recorded: string, run: () => { got: string; truth: string }) => {
  let got: string;
  let truth: string;
  try { ({ got, truth } = run()); } catch (e) { got = "threw " + String(e).split("\n")[0]; truth = "?"; }
  const ok = got === recorded && got !== truth;
  if (ok) reproduced++; else stale++;
  console.log((ok ? "XFAIL reproduced  " : "NOT REPRODUCED    ") + id + ": smallest=" + got + " true-min=" + truth + " (recorded " + recorded + ")");
};

// Inward admission: TypeScript's documented unsoundness delivers non-evidence
// to an exported evidence parameter without forging the brand.

// R1 spread override: the unique-symbol brand key survives a spread; one field
// is replaced. (E6 in consumer.mts only shows that a literal lacking the brand
// does not acquire it.)
record("R1 inward, spread override", "c", () => {
  const forged = { ...sorted, a: users.a };
  return { got: smallest(forged).name, truth: trueMin(forged) };
});

// R2 array covariance: a SortedBy[] viewed as Three<User>[] accepts any Three.
record("R2 inward, array covariance", "c", () => {
  const ev: SortedBy<Three<User>, User_compare>[] = [sorted];
  const view: Three<User>[] = ev;
  view[0] = sort(users, reversed);
  return { got: smallest(ev[0]).name, truth: trueMin(ev[0]) };
});

// R3 method bivariance: a method slot typed over Three<User> accepts `smallest`.
interface Picker { pick(xs: Three<User>): User }
record("R3 inward, method bivariance", "c", () => {
  const p: Picker = { pick: smallest };
  const xs = sort(users, reversed);
  return { got: p.pick(xs).name, truth: trueMin(xs) };
});

// X2 mutable property covariance: a wider view of a record slot writes a
// non-evidence value into an evidence-typed slot.
record("X2 inward, property covariance", "c", () => {
  const box: { v: SortedBy<Three<User>, User_compare> } = { v: sorted };
  const wide: { v: Three<User> } = box;
  wide.v = sort(users, reversed);
  return { got: smallest(box.v).name, truth: trueMin(box.v) };
});

// Stable payload: form (i) mints genuine evidence over data the caller can
// still change. A1 and A2 then pass the mutated value back into the exported,
// trusting `smallest`, so requirement 2 alone would also refuse them.

// A1 re-establishment over an alias: checkSortedUsers returns the caller's own
// object, and the spread dropped `readonly`.
record("A1 payload, re-establishment alias", "c", () => {
  const mutable = { ...sorted };
  const evidence = checkSortedUsers(mutable);
  mutable.a = User.make("c", 30n);
  return evidence ? { got: smallest(evidence).name, truth: trueMin(evidence) } : { got: "refused", truth: "refused" };
});

// A2 an element alias: the triple is frozen, its elements are not.
record("A2 payload, element alias", "a", () => {
  const a = { ...User.make("a", 10n) };
  const evidence = sortUsers(three(a, User.make("b", 20n), User.make("c", 30n)));
  a.age = 100n;
  return { got: smallest(evidence).name, truth: trueMin(evidence) };
});

// A3 an accessor read after the re-check (TOCTOU), isolating requirement 3.
// smallestChecked already has requirement 2's shape: an unbranded parameter and
// a form (i) re-check on entry before its body trusts the value. The check reads
// `a` once and sees "a"; the body's read sees "c". No unsound route is
// involved: the re-check is defeated because it runs over the caller's object,
// not over a snapshot taken first.
record("A3 payload, accessor after re-check", "c", () => {
  const a = User.make("a", 10n), b = User.make("b", 20n), c = User.make("c", 30n);
  let reads = 0;
  const tricky: Three<User> = { ...three(a, b, c), get a() { return reads++ < 1 ? a : c; } };
  const got = smallestChecked(tricky);
  return got ? { got: got.name, truth: trueMin(tricky) } : { got: "refused", truth: "refused" };
});

// Canonical method: form (i) reads User.compare through the mutable exported
// companion at call time, and Object.assign ignores `readonly`. The patch runs
// inside each callback (it is idempotent), so a frozen companion throws there.
record("R10 canonical, companion patch, form (i)", "c", () => {
  Object.assign(User, { compare: reversed });
  const evidence = sortUsers(users);
  return { got: smallest(evidence).name, truth: trueMin(evidence) };
});
record("R10 canonical, companion patch, re-establishment", "c", () => {
  Object.assign(User, { compare: reversed });
  const evidence = checkSortedUsers(three(User.make("c", 30n), User.make("b", 20n), User.make("a", 10n)));
  return evidence ? { got: smallest(evidence).name, truth: trueMin(evidence) } : { got: "refused", truth: "refused" };
});

console.log("expected-fail: " + reproduced + " reproduced, " + stale + " not reproduced (input; not acceptance)");
// No @types/node here: an uncaught throw is how this file exits non-zero.
if (stale > 0) throw new Error(stale + " expected-fail case(s) no longer reproduce; update the record and note §2.10");

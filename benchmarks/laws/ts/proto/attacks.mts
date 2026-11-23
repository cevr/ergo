// Attacks on the laws prototype's evidence boundary, covering the four
// rules from spike-where round 3 plus the laundering route. Strict, no any.
// Every type-level refusal is an @ts-expect-error naming its code (opened by
// ../tools/open-negatives.cjs); every attack that type-checks must be refused
// at runtime with EF186 or give the correct answer. The script exits 1 when
// an attack yields a wrong answer.
import * as lib from './lib.mjs';
import { makeUser, makeTrio, sortTrio, sortTrioBy, sortTrioLater, search } from './lib.mjs';
import type { SortedBy, Trio, User } from './lib.mjs';

const ada = makeUser("ada", 36n);
const bob = makeUser("bob", 12n);
const cy = makeUser("cy", 24n);
const trio = makeTrio(ada, bob, cy);
const sorted = sortTrio(trio);
const reversed = (a: User, b: User): bigint => b.age - a.age;
const results: string[] = [];
let wrong = 0;
// refused: the attack must throw EF186 (or the named TypeError) and never answer.
const refused = (label: string, needle: string, run: () => unknown) => {
  try {
    const got = run();
    results.push("WRONG " + label + " answered " + String(got));
    wrong++;
  } catch (e) {
    const text = String(e);
    if (text.includes(needle)) results.push("refused " + label + ": " + text.split("\n")[0]);
    else { results.push("WRONG " + label + " threw " + text.split("\n")[0]); wrong++; }
  }
};
// correct: the attack runs but cannot change the answer.
const correct = (label: string, want: unknown, run: () => unknown) => {
  try {
    const got = run();
    if (got === want) results.push("correct " + label + " = " + String(got));
    else { results.push("WRONG " + label + " = " + String(got) + ", want " + String(want)); wrong++; }
  } catch (e) {
    results.push("WRONG " + label + " threw " + String(e).split("\n")[0]);
    wrong++;
  }
};

// L1 laundering (coordinator, N1): TypeScript passes a reversed comparator to
// the exported sort and tries to keep the result as evidence. The rule (ii)
// export returns the carrier, so naming it SortedBy does not type-check ...
// @ts-expect-error TS2322 L1 a sortTrioBy result is not SortedBy
const laundered: SortedBy<Trio, "compareUsers"> = sortTrioBy(trio, reversed);
void laundered;
// ... and handing it straight to the evidence-requiring export type-checks
// (the parameter is unbranded) but the adapter re-checks and refuses it.
refused("L1-laundering-through-search", "EF186", () => search(sortTrioBy(trio, reversed), ada));

// Inward rule: host-originated evidence is never trusted.
// R1 spread override: copy genuine evidence, replace a field.
refused("R1-spread-override", "EF186", () => search({ ...sorted, first: makeUser("zed", 99n) }, ada));
// R2 array covariance: an evidence array viewed as Trio[] accepts any trio.
refused("R2-array-covariance", "EF186", () => {
  const ev: SortedBy<Trio, "compareUsers">[] = [sorted];
  const view: Trio[] = ev;
  view[0] = trio;
  return search(ev[0]!, ada);
});
// R3 method bivariance: an interface method slot typed over the carrier,
// filled with the evidence-requiring export.
interface Searcher { find(xs: Trio, x: User): boolean }
refused("R3-method-bivariance", "EF186", () => {
  const s: Searcher = { find: search };
  return s.find(trio, ada);
});
// X2 mutable property covariance: a holder of evidence viewed as a holder of trios.
refused("X2-property-covariance", "EF186", () => {
  const holder: { xs: SortedBy<Trio, "compareUsers"> } = { xs: sorted };
  const view: { xs: Trio } = holder;
  view.xs = trio;
  return search(holder.xs, ada);
});

// Stability rule: evidence covers a deep, Effra-owned snapshot.
// The `as never as` casts below stand for a JavaScript caller, which builds
// records without the declaration brand.
// A1 recheck alias: mutate the argument after the call; the snapshot is unaffected.
correct("A1-recheck-alias", true, () => {
  const host = { first: { name: "bob", age: 12n }, second: { name: "cy", age: 24n }, third: { name: "ada", age: 36n } };
  const out = sortTrio(host as never as Trio);
  host.first.age = 99n;
  return out.first.age === 12n && search(out, makeUser("bob", 12n));
});
// A2 element alias: mutate a shared element of a trio after it was sorted.
correct("A2-element-alias", true, () => {
  const shared = { name: "bob", age: 12n };
  const out = sortTrio(makeTrio(ada, shared as never as User, cy));
  shared.age = 1000n;
  return out.first.age === 12n && Object.isFrozen(out.first);
});
// Returned evidence cannot be mutated in place.
refused("A3-mutate-evidence", "TypeError", () => {
  const out = sortTrio(trio);
  (out.first as { age: bigint }).age = 99n;
  return out.first.age;
});
// G1 getter: an accessor that answers differently on each read is refused
// before it is ever called.
let getterReads = 0;
refused("G1-getter", "EF186", () => {
  const tricky = { second: cy, third: ada, get first() { getterReads++; return getterReads === 1 ? bob : makeUser("zed", 99n); } };
  return search(tricky as never as Trio, bob);
});
correct("G1-getter-reads", 0, () => getterReads);
// A3 read count (spike-where r5-n1): a proxy whose data properties answer
// differently on every read. The output alone cannot show requirement 3,
// because an extra entry check over the caller's object can still return the
// right answer; so the acceptance counts how often each field of the
// argument is read. The adapter must read each field once, into the
// snapshot, and run the admission check and the body on that snapshot.
correct("A3-read-count", "true first:1 second:1 third:1", () => {
  const fields = ["first", "second", "third"];
  const honest: Record<string, User> = { first: bob, second: cy, third: ada };
  const hostile: Record<string, User> = { first: makeUser("zed", 99n), second: bob, third: bob };
  const counts: Record<string, number> = {};
  const read = (key: string | symbol): User | undefined => {
    if (typeof key !== "string" || !fields.includes(key)) return undefined;
    counts[key] = (counts[key] ?? 0) + 1;
    return counts[key] === 1 ? honest[key] : hostile[key];
  };
  const target = { first: bob, second: cy, third: ada };
  const proxy = new Proxy(target, {
    get: (t, key) => read(key) ?? Reflect.get(t, key),
    getOwnPropertyDescriptor: (t, key) => {
      const v = read(key);
      return v === undefined ? Reflect.getOwnPropertyDescriptor(t, key) : { value: v, writable: true, enumerable: true, configurable: true };
    },
  });
  const found = search(proxy as never as Trio, bob);
  return String(found) + " " + fields.map((k) => k + ":" + String(counts[k] ?? 0)).join(" ");
});
// A3 nested read count (review 2, o2): sortedness is decided one level down,
// by each User's age, so the count covers the nested fields too. Every read
// after the first answers a hostile value; the adapter must read each nested
// field once, into the snapshot, by any route (descriptor or get).
correct("A3-nested-read-count", "true bob.age:1 bob.name:1 cy.age:1 cy.name:1 ada.age:1 ada.name:1", () => {
  const counts: Record<string, number> = {};
  const nested = ["age", "name"];
  const counted = (label: string, user: User): User => {
    const target: Record<string, unknown> = { name: user.name, age: user.age };
    const read = (key: string | symbol): { hit: boolean; value?: unknown } => {
      if (typeof key !== "string" || !nested.includes(key)) return { hit: false };
      const k = label + "." + key;
      counts[k] = (counts[k] ?? 0) + 1;
      return { hit: true, value: counts[k] === 1 ? target[key] : key === "age" ? 99n : "zed" };
    };
    return new Proxy(target, {
      get: (t, key) => { const r = read(key); return r.hit ? r.value : Reflect.get(t, key); },
      getOwnPropertyDescriptor: (t, key) => {
        const r = read(key);
        return r.hit ? { value: r.value, writable: true, enumerable: true, configurable: true } : Reflect.getOwnPropertyDescriptor(t, key);
      },
    }) as never as User;
  };
  const hostile = { first: counted("bob", bob), second: counted("cy", cy), third: counted("ada", ada) };
  const found = search(hostile as never as Trio, bob);
  const order = ["bob", "cy", "ada"].flatMap((u) => nested.map((k) => u + "." + k));
  return String(found) + " " + order.map((k) => k + ":" + String(counts[k] ?? 0)).join(" ");
});

// Canonical-binding rule: adapters call module-private bindings, the module
// namespace is frozen, and exported functions do not reach the adapters.
// R10 companion patch: Object.assign onto the module namespace throws.
refused("R10-namespace-patch", "TypeError", () => Object.assign(lib, { compareUsers: reversed }));
// R10b patching call/apply on the exported compare does not reach search,
// which calls the module-private binding directly.
correct("R10b-function-property-patch", true, () => {
  Object.assign(lib.compareUsers, { call: reversed, apply: reversed });
  return search(sorted, cy) && !search(sorted, makeUser("dee", 99n));
});

// Fillers that type-check through never.
const none: never[] = [];
refused("N1-never-filler", "EF186", () => search(none[0], ada));
const viaRest: (...rest: never[]) => boolean = search;
refused("N2-never-rest", "EF186", () => viaRest());
// An untyped value (JSON.parse returns the any type; no any keyword is written).
refused("N3-json-any", "EF186", () => search(JSON.parse('{"first":{"name":"x","age":1}}'), ada));

// Type-level: the outward brand cannot be forged without a cast.
// @ts-expect-error TS2322 F1 a plain trio is not SortedBy
const forged: SortedBy<Trio, "compareUsers"> = trio;
void forged;
// @ts-expect-error TS2322 F2 evidence for one comparator is not evidence for another
const otherIndex: SortedBy<Trio, "reversed"> = sorted;
void otherIndex;
// @ts-expect-error TS2322 F3 an effect export's recipe is not itself evidence; only its success value is
const recipeAsEvidence: SortedBy<Trio, "compareUsers"> = sortTrioLater(trio);
void recipeAsEvidence;

console.log(results.join("\n"));
if (wrong > 0) { console.log(wrong + " attack(s) produced a wrong answer"); throw new Error("probe failed"); }
console.log("attacks ok: every attack refused or harmless");

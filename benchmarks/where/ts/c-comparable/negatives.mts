// Direct misuse of the (c) comparable projection is refused.
import { makeUser, makeUserId, openFile, three, sort3, empty, insert } from './lib.mjs';
import type { HashMap, File, UserId, Key } from './lib.mjs';

// @ts-expect-error TS2345 N1 a record is not Ordered
sort3(three(makeUser("c", 3n), makeUser("a", 1n), makeUser("b", 2n)));
// @ts-expect-error TS2345 N2 a function-keyed map
const fnKeys = insert(empty<() => void, string>(), () => {}, "v");
// @ts-expect-error TS2345 N3 a resource-handle key (File)
const fileKeys = insert(empty<File, string>(), openFile("x"), "v");
// @ts-expect-error TS2345 N4 a record with a callable field
const callbackKeys = insert(empty<{ readonly id: bigint; readonly run: () => void }, string>(), { id: 1n, run: () => {} }, "v");
const byId: HashMap<UserId, string> = empty<UserId, string>();
// @ts-expect-error TS2345 N5 a key of another type
insert(byId, makeUser("a", 1n), "v");
// @ts-expect-error TS2345,TS2589 N6 a generic caller that does not restate the constraint
const single = <K,>(k: K) => insert(empty<K, string>(), k, "v");
// @ts-expect-error TS2345,TS2589 N7 a generic caller that forwards the check as Key<K>
const forwarded = <K,>(k: Key<K>) => insert(empty<K, string>(), k, "v");
// Accepted, not refused: f64 is Ordered, so NaN reaches the comparison.
const floats = sort3(three(1.5, Number.NaN, 0.5));
export { fnKeys, fileKeys, callbackKeys, single, forwarded, floats, makeUserId };

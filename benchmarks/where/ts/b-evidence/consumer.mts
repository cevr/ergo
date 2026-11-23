// The laws spike input evidence, not acceptance (note §2.10): a TypeScript caller under
// the outward evidence boundary rule. attacks-expected-fail.mts records the
// routes this projection does not stop.
import { User, three, sortUsers, checkSortedUsers, sort, sortTwice, smallest } from './lib.mjs';

const users = three(User.make("c", 30n), User.make("a", 10n), User.make("b", 20n));
const reversed = (self: User, other: User) => other.age - self.age;

// Form (i): evidence crosses outward; the comparator is Effra's.
const sorted = sortUsers(users);
// Form (ii): the caller's comparator is used, and the result is unbranded.
const mine = sort(users, reversed);
// Re-establishment: Effra checks with the canonical method before minting.
const recheckedOk = checkSortedUsers(sorted);
const recheckedMine = checkSortedUsers(mine);
console.log([
  "form(i) smallest=" + smallest(sorted).name,
  "form(ii) order=" + [mine.a, mine.b, mine.c].map(u => u.name).join(""),
  "recheck sorted=" + (recheckedOk ? smallest(recheckedOk).name : "refused"),
  "recheck reversed=" + (recheckedMine ? smallest(recheckedMine).name : "refused"),
].join(" "));

const negatives = () => {
// @ts-expect-error TS2554 E1 form (i) takes no method argument: the caller cannot choose the comparator
sortUsers(users, reversed);
// @ts-expect-error TS2345 E2 laundering: form (ii) returns the unbranded Three<User>, which a trusting consumer refuses
smallest(sort(users, reversed));
// @ts-expect-error TS2345 E3 laundering through generic forwarding: also unbranded
smallest(sortTwice(users, reversed));
// @ts-expect-error TS2345 E4 an unchecked value is not evidence
smallest(users);
// @ts-expect-error TS2345 E5 a re-establishment result must be checked before use
smallest(checkSortedUsers(mine));
// @ts-expect-error TS2345 E6 a literal that lacks the brand does not acquire it (spreading genuine evidence keeps it: expected-fail R1)
smallest({ ...users });
};
export { negatives };

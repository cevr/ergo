// The laws spike INPUT EVIDENCE, NOT ACCEPTANCE (note §2.10). The evidence mechanism is
// The laws spike's; this fixture models only the outward boundary rule, under which an
// exported function that mints evidence takes one of two forms in the .d.mts.
// It does not model inward admission, a stable payload or a private canonical
// method: attacks-expected-fail.mts records those routes as expected failures.
//
//   (i)  It binds the canonical methods on the Effra side. Its signature has
//        no method parameter, so a TypeScript caller cannot choose the
//        comparator, and the evidence it returns crosses outward.
//   (ii) It accepts the methods from its caller (a where clause or a generic
//        forwarding function). Its projection drops the evidence and returns
//        the unbranded carried type; Effra re-establishes the evidence before
//        trusting a value that comes back.
//
// Evidence is a type-only brand keyed by an unexported unique symbol and
// named by the canonical method's declaration identity. The runtime value is
// the carried value itself.
import type { User, Three } from '../b-witness/lib.mjs';
export type { Three } from '../b-witness/lib.mjs';
export { User, three } from '../b-witness/lib.mjs';

// The declaration identity of User's canonical compare.
declare const __ef_method_User_compare: unique symbol;
export type User_compare = typeof __ef_method_User_compare;

declare const __ef_brand_evidence_SortedBy: unique symbol;
export type SortedBy<S, By> = S & { readonly [__ef_brand_evidence_SortedBy]: readonly [(by: By) => By] };

// Form (i). fn sortUsers(xs: Three<User>) -> SortedBy<Three<User>, User.compare>
// Effra resolves User.compare inside; nothing to pass.
export declare const sortUsers: (xs: Three<User>) => SortedBy<Three<User>, User_compare>;

// Form (i) again: re-establishment. Checks the order with the canonical
// method and mints only when it holds.
export declare const checkSortedUsers: (xs: Three<User>) => SortedBy<Three<User>, User_compare> | undefined;

// Form (ii). fn sort<T: type>(xs: Three<T>) -> SortedBy<Three<T>, T.compare> where T: Ordering
// The method arrives from the caller, so the evidence is dropped: Three<T>.
export declare const sort: <T>(xs: Three<T>, compare: (self: T, other: T) => bigint) => Three<T>;

// Form (ii) for generic forwarding: a generic function that calls `sort`
// with its own method argument mints nothing that reaches TypeScript.
export declare const sortTwice: <T>(xs: Three<T>, compare: (self: T, other: T) => bigint) => Three<T>;

// A consumer that trusts the evidence: returns the smallest without checking.
export declare const smallest: (xs: SortedBy<Three<User>, User_compare>) => User;

// Requirement 2's shape (inward admission): the parameter is unbranded, and
// the body trusts the value only after re-establishing the evidence on entry
// through form (i). It takes no snapshot first (requirement 3), which the
// expected-fail case A3 exploits.
export declare const smallestChecked: (xs: Three<User>) => User | undefined;

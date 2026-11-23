// Counterfactual: ../proto/lib.d.mts with search taking branded evidence (naive trust). Derived by hand.
import type { Context, Effect, Fiber } from 'effect';
export interface File {readonly _effraFile: "File"}
export interface Latch {readonly _effraLatch: "Latch"}
declare const __ef_brand_User: unique symbol;
export interface User { readonly [__ef_brand_User]: "User"; readonly name: string; readonly age: bigint; }
declare const __ef_brand_Trio: unique symbol;
export interface Trio { readonly [__ef_brand_Trio]: "Trio"; readonly first: User; readonly second: User; readonly third: User; }
export interface AssertRequirement { readonly _effraService: "Assert" }
export type AssertProvider = { readonly "check": (arg_condition: boolean, arg_message: string) => Effect.Effect<void, { readonly _tag: "AssertionFailed" }, never>; readonly "equalText": (arg_actual: string, arg_expected: string) => Effect.Effect<void, { readonly _tag: "AssertionFailed" }, never>; };
declare const __ef_service_Assert: Context.Service<AssertRequirement, { readonly "check": (arg_condition: boolean, arg_message: string) => Effect.Effect<void, { readonly _tag: "AssertionFailed" }, never>; readonly "equalText": (arg_actual: string, arg_expected: string) => Effect.Effect<void, { readonly _tag: "AssertionFailed" }, never>; }>;
export { __ef_service_Assert as Assert };
export interface ConsoleRequirement { readonly _effraService: "Console" }
export type ConsoleProvider = { readonly "log": (arg_message: string) => Effect.Effect<void, never, never>; };
declare const __ef_service_Console: Context.Service<ConsoleRequirement, { readonly "log": (arg_message: string) => Effect.Effect<void, never, never>; }>;
export { __ef_service_Console as Console };
export interface ClockRequirement { readonly _effraService: "Clock" }
export type ClockProvider = { readonly "sleep": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; };
declare const __ef_service_Clock: Context.Service<ClockRequirement, { readonly "sleep": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; }>;
export { __ef_service_Clock as Clock };
export interface SchedulerRequirement { readonly _effraService: "Scheduler" }
export type SchedulerProvider = { readonly "sleep": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; readonly "advance": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; readonly "awaitRegistration": () => Effect.Effect<void, never, never>; };
declare const __ef_service_Scheduler: Context.Service<SchedulerRequirement, { readonly "sleep": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; readonly "advance": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; readonly "awaitRegistration": () => Effect.Effect<void, never, never>; }>;
export { __ef_service_Scheduler as Scheduler };
export interface SyncRequirement { readonly _effraService: "Sync" }
export type SyncProvider = { readonly "latch": () => Effect.Effect<Latch, never, never>; readonly "await": (arg_latch: Latch) => Effect.Effect<void, never, never>; readonly "signal": (arg_latch: Latch) => Effect.Effect<void, never, never>; };
declare const __ef_service_Sync: Context.Service<SyncRequirement, { readonly "latch": () => Effect.Effect<Latch, never, never>; readonly "await": (arg_latch: Latch) => Effect.Effect<void, never, never>; readonly "signal": (arg_latch: Latch) => Effect.Effect<void, never, never>; }>;
export { __ef_service_Sync as Sync };
export interface FilesRequirement { readonly _effraService: "Files" }
export type FilesProvider = { readonly "openRead": (arg_path: string) => Effect.Effect<File, { readonly _tag: "IoError" }, never>; readonly "readText": (arg_file: File) => Effect.Effect<string, { readonly _tag: "IoError" }, never>; readonly "readFile": (arg_path: string) => Effect.Effect<string, { readonly _tag: "IoError" }, never>; };
declare const __ef_service_Files: Context.Service<FilesRequirement, { readonly "openRead": (arg_path: string) => Effect.Effect<File, { readonly _tag: "IoError" }, never>; readonly "readText": (arg_file: File) => Effect.Effect<string, { readonly _tag: "IoError" }, never>; readonly "readFile": (arg_path: string) => Effect.Effect<string, { readonly _tag: "IoError" }, never>; }>;
export { __ef_service_Files as Files };
export interface EnvRequirement { readonly _effraService: "Env" }
export type EnvProvider = { readonly "get": (arg_name: string) => Effect.Effect<string, never, never>; };
declare const __ef_service_Env: Context.Service<EnvRequirement, { readonly "get": (arg_name: string) => Effect.Effect<string, never, never>; }>;
export { __ef_service_Env as Env };
export interface RuntimeRequirement { readonly _effraService: "Runtime" }
export type RuntimeProvider = { readonly "inspect": () => Effect.Effect<string, never, never>; };
declare const __ef_service_Runtime: Context.Service<RuntimeRequirement, { readonly "inspect": () => Effect.Effect<string, never, never>; }>;
export { __ef_service_Runtime as Runtime };
export interface ForeignRequirement { readonly _effraService: "Foreign" }
export type ForeignProvider = { };
declare const __ef_service_Foreign: Context.Service<ForeignRequirement, { }>;
export { __ef_service_Foreign as Foreign };
declare const __ef_provider_Assertions: { readonly "check": (arg_condition: boolean, arg_message: string) => Effect.Effect<void, { readonly _tag: "AssertionFailed" }, never>; readonly "equalText": (arg_actual: string, arg_expected: string) => Effect.Effect<void, { readonly _tag: "AssertionFailed" }, never>; };
export {__ef_provider_Assertions as Assertions};
declare const __ef_provider_Stdout: { readonly "log": (arg_message: string) => Effect.Effect<void, never, never>; };
export {__ef_provider_Stdout as Stdout};
declare const __ef_provider_LiveClock: { readonly "sleep": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; };
export {__ef_provider_LiveClock as LiveClock};
declare const __ef_provider_TestClock: { readonly "sleep": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; };
export {__ef_provider_TestClock as TestClock};
declare const __ef_provider_LiveScheduler: { readonly "sleep": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; readonly "advance": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; readonly "awaitRegistration": () => Effect.Effect<void, never, never>; };
export {__ef_provider_LiveScheduler as LiveScheduler};
declare const __ef_provider_TestScheduler: { readonly "sleep": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; readonly "advance": (arg_milliseconds: bigint) => Effect.Effect<void, never, never>; readonly "awaitRegistration": () => Effect.Effect<void, never, never>; };
export {__ef_provider_TestScheduler as TestScheduler};
declare const __ef_provider_TestSync: { readonly "latch": () => Effect.Effect<Latch, never, never>; readonly "await": (arg_latch: Latch) => Effect.Effect<void, never, never>; readonly "signal": (arg_latch: Latch) => Effect.Effect<void, never, never>; };
export {__ef_provider_TestSync as TestSync};
declare const __ef_provider_LiveEnv: { readonly "get": (arg_name: string) => Effect.Effect<string, never, never>; };
export {__ef_provider_LiveEnv as LiveEnv};
declare const __ef_function_compareUsers: (arg_a: User, arg_b: User) => bigint;
export { __ef_function_compareUsers as compareUsers };
declare const __ef_function_makeUser: (arg_name: string, arg_age: bigint) => User;
export { __ef_function_makeUser as makeUser };
declare const __ef_function_makeTrio: (arg_first: User, arg_second: User, arg_third: User) => Trio;
export { __ef_function_makeTrio as makeTrio };
declare const __ef_function_sortWith: (arg_t: Trio, arg_compare: (arg0: User, arg1: User) => bigint) => Trio;
export { __ef_function_sortWith as sortWith };
declare class EfEvidence<E extends string, S, I extends string> { private readonly __ef_evidence: readonly [E, S, I]; }
export type SortedBy<S, I extends string> = S & EfEvidence<"SortedBy", S, I>;
declare const __ef_export_sortTrio: (arg_t: Trio) => SortedBy<Trio, "compareUsers">;
export { __ef_export_sortTrio as sortTrio };
declare const __ef_function_sortTrioBy: (arg_t: Trio, arg_compare: (arg0: User, arg1: User) => bigint) => Trio;
export { __ef_function_sortTrioBy as sortTrioBy };
declare const __ef_function_sortedTrio: (arg_t: Trio) => boolean;
export { __ef_function_sortedTrio as sortedTrio };
declare const __ef_export_search: (arg_xs: SortedBy<Trio, "compareUsers">, arg_x: User) => boolean;
export { __ef_export_search as search };
declare const __ef_function_oldest: (arg_t: Trio) => bigint;
export { __ef_function_oldest as oldest };

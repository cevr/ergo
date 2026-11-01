# Types

Definitions of every term used here are in the [glossary](../../GLOSSARY.md). Each section carries its status.

Types are nominal. Two named types are identical only when they are the same declaration. Which value goes where is one relation, [Assignability](#assignability). *Prior art:* Go's named types and implicit interface satisfaction; Gleam and ReScript custom types are nominal too.

## Assignability

Status: **checked** by `internal/sketch/check` as one relation, `assignability`, and **lowered** from the fact it records (check-v1 stage D3a).

```go
type Ints []int
type Store { Get() int }
type Count int
func Get(c Count) int { int(c) }

let raw []int = named      // clause 2: named is an Ints
let s Store = Count(3)     // clause 3: Count implements Store
let o Option[int] = None   // clause 4
```

1. **The clauses, in Go's order.** A value of type `V` is assignable to type `T` by the first clause that holds, and each clause names the implicit conversion the assignment makes, the fact `Info.Implicit` records at the value:
   1. `V` and `T` are identical. No conversion.
   2. `V` and `T` have identical underlying types, at least one of them has no name, and neither is a type parameter. A declared type, a basic type and a type parameter have a name; a slice, Map, function or tuple type written in place has none. No conversion: `let raw []int = named` and `let named Ints = raw` cross, `Ints` to another `type Other []int` does not.
   3. `T` is an operation type and `V` implements it ([Interface assignability](#interface-assignability)), whatever `V`'s name. `ConvOperations` with the function that provides each operation, or no conversion when `V` is itself an operation type's value. Check implementation first, so a missing operation is the diagnostic when both checks fail. Then refuse a `V` that holds a `Task` when `T` holds none ([Task ownership](#task-ownership)), so `Phantom[Task[int, Fault]]` still admits a `Task`.
   4. `V` is a variant of the union `T` ([Unions](#unions) 2), or a row value whose every error type is a variant of `T`. `ConvVariant` or `ConvRow`.
   5. `V` and `T` are function types with the same parameters and results, `V`'s error and `needs` rows are subsets of `T`'s, and at least one of them has no name ([Function values](#function-values) 1). No conversion: a function value has one Go shape.

   Nothing else is assignable (S108). Two named types cross by no clause unless clause 3 or 4 relates them: `type A Mem` and `type B Mem` over one record, `Count` and `int`, `type F func(n int) int` and `type G func(n int) int` each need a conversion ([Conversions](#conversions)), and the message names it only where one exists.
2. **Every site.** One relation decides every place a value is assigned: a `let` (single, and per element of a multi-name `let`, whose element conversions `Info.ImplicitElems` records), an argument, a result, a pattern's written type, a composite literal's field and element, the two operands of `==` and `!=` (one goes into the other's type), and a `wire` value. The lowering reads the fact and never derives a conversion again.
3. **Comparison through operations.** A value that implements an operation type through its functions is not that type's value until it is assigned, so `s == Count(1)` with `s Store` is S108; assign it first.
4. *Prior art:* go/types `operand.assignableTo` has clauses 1 to 3 in this order, and cmd/compile's `typecheck.assignOp` returns the lowering operation (`OCONVNOP`, `OCONVIFACE`) with the reason from the same clauses, as `assignability` returns the conversion. Gleam and ReScript have no defined types over collections (an opaque type converts through functions); Rust newtypes cross only by `.0` or `From`.

## Task ownership

Status: **checked** (check-v1 stage D3a). [runtime.md](runtime.md#tasks) rule 3 defines which types hold a Task and the type positions that admit them.

1. A Task-holding value flows only into these uses: a local `let` in its own frame; a return (an explicit return or a body's final value); a call argument; or the direct receiver of a called `Wait` or `Stop`. A dot call to a package function uses its receiver as the first argument and follows the argument rule. Assignment checks the parameter type; clause 3 refuses erasing a Task into an operation type that holds none. Passing it to a type parameter, as in `id(t)` for a generic `id`, remains admitted until stage E's type-parameter rule.
2. Parentheses, `try`, `catch` and `go` are transparent wrappers: the wrapped value must flow into an allowed use. They do not make a discarded Task legal. A final value can flow through `if`, `match`, `catch`, `try` or `with` into a return or another allowed sink.
3. Every other use is S108. A reference to a Task-holding local from another frame is also S108, wherever it stands. Function literals, trailing blocks other than `with`, and `go` blocks open frames. A refused type covers values assigned into it, so later uses add no diagnostic.
4. **Lowering** (check-v1 stage D3b; [stdlib.md](stdlib.md#v1-packages-task-scope-queue-and-slice), Hidden arguments). A function whose result is a `Task` takes the caller's frame scope as a hidden destination and moves the Task there on return, so the Task outlives its own frame and the caller's scope stops and drains it if nobody waits. A result that holds a Task inside another type, and a Task as a type argument, are refused with S109 naming check-v1 stage E.

These two uses are refused even though they mention the Task's local frame:

```go
// In a function that holds t:
t = task.Go() { work() }       // reassignment is not an allowed use
(func() { t.Stop() })()        // even an immediately called literal is another frame
```

4. *Prior art:* Rust checks ownership across scoped threads and trait objects; Elixir's Task checks its owner at run time. Gleam and ReScript have no Task ownership rule in their types. Ergo checks the frame and allowed uses before lowering.

## Basic types

Status: **implemented in prototype** for `string`, `int64` and `bool`; **partly implemented** for `duration`, which the prototype types only as a literal and as the `timeout.After` argument (README Gap 25).

1. `string` holds bytes. A literal's value is the bytes its spelling decodes to ([grammar.md](grammar.md#literals)).
2. `int64` is the only integer type with checked meaning in v0 (README Gap 9). Non-constant arithmetic wraps; constant overflow is an error.
3. `bool` is the type of comparisons and `if` conditions.
4. `duration` is the type of duration literals. It is a basic type, usable in every type position: parameters, results, fields and type arguments. Its name is predeclared. Prototype: `duration` as a type name is S104, and the name is reserved as a source name (S109).
5. Designed, with [#81](https://github.com/cevr/ergo/issues/81) and [#211](https://github.com/cevr/ergo/issues/211): Go's other numeric types, `byte`, `rune`, floating point, slices `[]T`, maps `Map[K, V]`, queues `queue.Queue[T]` ([runtime](runtime.md#channels)), function types and the prelude `Option[T]`.
6. **Literals are typed where they appear** (196u; checked by `internal/sketch/check`). A literal takes the type its context expects: a written `let` type, a parameter (after the type arguments that typed arguments fixed), a result (a `return` or a tail value), a record field, a slice element, a Map key or value, the other operand of a binary operator or of `..`, an assignment's target, an index or slice bound (`int`), and a constant `case` (the subject's type). An integer literal may take any integer or float type, a float literal only a float type, a rune literal any integer type, and a string literal a string type; an integer literal is never a `duration` (S108 "an integer literal is not a duration: write a duration literal such as 5s"). Every other position gives no expected type, and the literal takes its default: `int`, `float64`, `rune`, `string`, `duration`, and `bool` for `true` and `false`. A literal that cannot take the expected type is S108 naming the form to write ("a float literal takes only a float type, not int; write 2").
7. **A name has one type.** A package-level `let` has one type, and no name converts by itself: `let b int8 = Limit` with `let Limit = 10` is S108 "Limit is int; write int8(Limit) or declare Limit int8". Constant expressions evaluate exactly, step by step, and each typed step must fit its type: an overflow is S108 ("the constant 200 overflows int8" for `let y int8 = 100 + 100`, and for `100 + 100 - 100`, whose first step overflows), as in Rust's constant evaluation. A literal fits its type after its sign, in parentheses or not, so `-128` and `-(128)` are `int8`. Ergo has no untyped constants: Go's untyped kinds, their defaulting order and the contextual types Go records afterwards are the counter-example (#196, 196u "Why"). *Prior art:* Rust and Swift type a literal from its context with `i32` and `Int` defaults, and `1 + 2.5` is an error in Rust; MoonBit overloads an integer literal by its expected type (`let x : Int64 = 1`); Gleam, and ReScript after OCaml, keep `Int` and `Float` apart with separate operators (`+`, `+.`); Elixir's numbers are dynamic.

## Zero values and definite initialisation

Status: **implemented in prototype** for struct literals; **designed** for the rest.

1. A type is *defaultable* when it has a valid zero value: a basic type, or a struct whose fields are all defaultable. A union, a capability, a function type, a `Task`, a resource, a borrow, and a struct that contains any of them are not defaultable. Outside its package, a struct type with an unexported field is not defaultable either (rule 6).
2. A keyed struct literal must set every field whose type is not defaultable (S108). A positional literal sets every field.
3. Designed: a variable of a non-defaultable type must be initialised before use. `let p Payment` with no value is an error. *Prior art:* Swift and Kotlin definite initialisation; Go instead gives every type a zero value, which is how a nil interface reaches a type switch.
4. Designed: an operation that invents zero values needs a defaultable element type. The decided language has no such built-in (`make`, the comma-ok forms and channels are gone, #196); the library returns `Option` instead, as `m.Lookup(k)` returns `Option[V]`.
5. There is no `nil`. *Prior art:* Gleam, ReScript and Rust have no null; Go's typed-nil interface and nil map are its most cited traps.
6. **Construction authority (designed).** Outside the package that declares it, a struct type with an unexported field ([grammar.md](grammar.md#visibility)) is neither defaultable nor literal-constructible. Each of these is an error outside the package:
   - a composite literal of the type, including the empty literal `Quantity{}`;
   - an implicit zero value: `let q Quantity` with no value, a zero-filled field of type `Quantity` in an enclosing struct's literal or zero value, and the zero value of a map lookup that misses (use `m.Lookup(k)`, which returns `Option`).

   Inside the declaring package, the construction-authority restriction is lifted, and the ordinary rules 1 to 4 still apply. A struct with an unexported field of a non-defaultable type stays non-defaultable in its own package:

   ```go
   type State = Idle | Running
   type Idle {}
   type Running { Since int64 }
   type Box { state State }

   func newBox() Box { return Box{state: Idle} } // allowed: a literal inside the package
   func zeroBox() Box {
   	let b Box // error even inside the package: State is a union, so Box is not defaultable (rule 3)
   	return b
   }
   ```

   Values from outside come only from the functions the package exports, so `ParseQuantity` is the only way to build a `Quantity` and its check cannot be bypassed. Exports stay by capitalisation. *Prior art:* Rust forbids constructing a struct with private fields outside its module and gives it no `Default` unless the module derives or implements one; Gleam's `opaque` types hide constructors; Go leaves this hole open: a two-package probe builds `Quantity{}` and `make([]Quantity, 1)` from another package and reads `units=0` for both (review round 2 receipt).

## Structs

Status: **implemented in prototype**.

```go
type Order {
	ID    string,
	Total int64,
}

let order = Order{ID: "42", Total: 1999}
let same = Order{"42", 1999}
```

1. Field names in one struct are distinct (S108). A field and a function on the type (see [Methods](#methods)) may share a name: `x.f` is the field first (196m).
2. A composite literal is all keyed or all positional (S001). A positional literal lists every field in order (S108). A keyed literal sets each field at most once (S108) and names only declared fields (S104). **A record with no fields is written bare** (check-v1 D-B2): its name is its one value, `return Pending`, and `Pending{}` is S108 "a record with no fields is written bare: Pending"; a bare name of a record with fields is S108 "Paid has fields; write Paid{…}". A function on such a record is called on the bare name, `Runner.Each(f)`: there are no method expressions to confuse it with. In a pattern the bare name tests the type ([rows.md](rows.md#totality), Totality 1). *Principle:* one form per job. *Prior art:* Rust unit structs, Gleam fieldless constructors and ReScript constant constructors are bare.
3. Each field value must be assignable to the field's type (S108). A field value whose failure is not handled is S105. A field value that is a composite literal writes its type, keyed or positional: `Wrap[S]{value: {h: 1}}` is S108 "missing type in composite literal; write S{h: 1}" (the literal's source is `{…}` past 40 characters or across a line). As in Go, a composite literal may elide its type only as an element of an array, slice or map literal or as a map literal's key (Go spec, "Composite literals"; #196 round 17 C1). *Prior art:* go/types reports "missing type in composite literal" there, and Ergo adds the fix; Rust and Gleam always name a record's constructor.
4. A struct must not contain itself by value, directly or through other structs or tuple elements (S108). Recursion behind a union is allowed, because a union value is a reference in the lowered Go. *Prior art:* Swift's `indirect enum` boxes recursion; Go rejects an invalid recursive type.
5. A struct with a resource field is itself a resource ([resources.md](resources.md#what-is-a-resource)).
6. Designed: struct embedding is allowed only in `wire` and in interfaces. Value semantics hold: a copy is a snapshot, and move-on-last-use keeps it cheap.

## Unions

Status: **checked** by `internal/sketch/check` (check-v1 stage B); **implemented in prototype** for bare-name variants declared in the same file.

```go
type Payment = Pending | Authorized | Declined{ Reason string }

type Pending {}
type Authorized { AuthID string }

type Option[T] = Some[T]{ Value T } | None{}
```

1. **Types by member shape** (check-v1 D-B1). Fields make a record, operations and laws an operation type, `=` a union, and `type N T` a defined type ([Defined types](#defined-types)); a type has one kind. A union lists one or more variants, each a record of this package. A bare-name variant names a record declared in the package: a basic, operation, union or defined type there is S108 "a union variant is a record declared in this package; main.Store is an operation type", and a record from another package is S109 naming 196b (packages). An inline variant `Declined{ Reason string }` declares that record in the package scope, usable as a type anywhere in the package; `Name{}` declares one with no fields. An inline variant of a generic union takes exactly the union's type parameters its fields mention, in the union's order (`Some[T]`, `None`). A variant may not be listed twice (S108). There are no type aliases: `type X = Y` is a one-variant union. *Prior art:* Gleam custom types and ReScript variants declare constructors inside the type, but those constructors are not types; Ergo's variants are record types so rows can share them (Koka, Effekt).
2. A value of a variant type is assignable to every union that lists it ([Assignability](#assignability) clause 4). A struct may be a variant of several unions (fixture `multiple-unions`).
3. A union has no zero value.
4. A `match` narrows a union value to its variants, nested through record patterns ([rows.md](rows.md#totality)).
5. The emitter generates a marker method per union on each variant, so a source method with that marker's name is an error in the prototype (fixture `marker-method`, S109).
6. **Lowering.** A union becomes a sealed Go interface with an unexported marker method, and each variant keeps its own struct fields. *Prior art:* Borgo lowers an enum to a tagged struct and matches on the tag; ReScript picks a representation per variant; Ergo keeps the interface form so a Go type switch narrows and each variant keeps its fields. The size trade-off between the two forms is not measured.

## Defined types

Status: **checked** by `internal/sketch/check` (check-v1 stage B) and **lowered** (check-v1 stage C): a Go defined type over the base type's Go type.

```go
type Celsius float64
type Span (int, int)
```

1. `type N T` defines a new nominal type over `T`, a basic, slice, Map, function, tuple or record type; a record instance (`type P2 Pair[int, int]`) and a variant (`type S2 Some[int]`) are record types. `N` is not identical to `T`. A value crosses between them by conversion ([Conversions](#conversions)), or, when `T` has no name (a slice, Map, function or tuple type written in place), by assignment ([Assignability](#assignability) clause 2): `let raw []int = named`. A defined type over a type parameter or over itself is S108. A defined type over any other type, an operation type (Task and the std operation types included), a union or an instance of a generic one, is S108 at the definition, "a defined type is over a basic, slice, Map, function, tuple or record type, and Store is an operation type; use Store directly", and its uses add no diagnostic (check-v1 stage D3a). A use in a list (a row entry, a type argument, a tuple element, a union variant, a wire key, a parameter, a field, a pattern of a case) makes that element invalid and keeps the others ([rows.md](rows.md) Inference 6 for rows). A defined type over a Task-holding record or slice, `type B Box[Task[int, Fault]]`, is S108 with the Task text ([runtime.md](runtime.md#tasks) rule 3). An operation type and a union keep one name each. *Prior art:* Gleam, ReScript and MoonBit have no newtype over a trait or a variant; Go has defined interface types.
2. A defined type over a tuple destructures like its tuple (`let (lo, hi) = span`); a defined type over a record keeps its braces, even with no fields.
3. Its operations are those of `T`: arithmetic on `Celsius`, comparison where `T` is comparable.
4. A defined type over a record, named in an error row, is an error type of its own ([Error types](#error-types)): it is thrown and caught as itself, by type test and by record pattern, and its text is its base record's. A constant of a defined type keeps that type: `func f() Flag { true }` is `Flag(true)` in Go.

## Tuples

Status: **checked** by `internal/sketch/check` (check-v1 D-B6, 196t).

```go
func DivMod(a, b int) (int, int) { return (a / b, a % b) }

let (q, r) = DivMod(7, 2)
let pair (int, string) = (q, "x")
```

1. A tuple type `(A, B, …)` has two or more elements and is structural: two tuple types are identical when their elements are. A tuple value is `(a, b)`.
2. A tuple is read only through a pattern ([rows.md](rows.md#totality)): `let (a, b) = t`, `for (k, v) in …`, `case (A, _):`. There is no `.0`.
3. A function has one result, which may be a tuple; a call is one value, so `f(g())` passes `g`'s tuple as one argument. `let q, r = e` destructures a tuple result. Tuple assignment does not convert its elements. Returning `pair()` from `(Mem, int)` as `(Store, int)` therefore names a repair when `Mem` implements `Store`: `let (m, n) = pair(); return (m, n)`, source on one line, with the call copied byte for byte, that checks in place of the body. There is no repair for a call written over more than one line. Each element of the returned literal takes its expected type. The checker chooses unbound names for the repair.
4. A tuple is comparable when its elements are, and defaultable when they are. It has no operations, so it implements no operation type.
5. *Prior art:* Gleam, ReScript and MoonBit tuples with pattern access; Elixir tuples; Go's several results exist mostly for `(T, error)`, which `fails` replaces.

## Capabilities

Status: **checked** by `internal/sketch/check` ([#196](https://github.com/cevr/ergo/issues/196)); runs.

```go
type Orders {
	Find(id string) Order fails OrderNotFound,
}
```

1. An interface with method signatures declares a capability. Method names in one capability are distinct (S108).
2. **A capability is not a first-class value.** The capability name denotes the provider that the nearest `with` binds. `Orders.Find(id)` calls that provider and charges `Orders` ([rows.md](rows.md#capability-rows)). Any other use of the name as an expression is an error: `Orders.Find` without a call (S108), and passing, returning, storing or capturing `Orders` (S104; README Gap 29).
3. **Capability name against interface type.** The same interface declaration has two roles, told apart by position:
   - in a `needs` clause, a `wire` entry, or as the receiver of a method call `Orders.m(…)`, the name is the **capability**: an ambient requirement that only `with` supplies;
   - in a type position (a parameter, result, field or type argument), the name is an ordinary **interface type**. A value of it is a provider value. Calling its method, `o.Find(id)`, runs that provider's method with the method's declared error row and charges no capability. A constructor that returns `Orders` returns such a value.

   This keeps decision #102: a capability is an ordinary interface bound by `wire`, and only the ambient binding is second-class. The checker implements both roles (README Gap 29); the build refuses a method call on an interface value until [#50](https://github.com/cevr/ergo/issues/50). *Prior art:* Effect separates a service's tag, the requirement, from a service value; Scala 3 capture checking treats capabilities apart from their types.
4. A capability method's signature carries its error row. A capability or interface method writes no capability row (S108), because a provider's requirements are captured at construction (README Gap 17).
5. The prelude capabilities `Console`, `Clock`, `Scheduler` and `Random` are ordinary interfaces bound by `wire` (decision #102). The prelude (`internal/sketch/std/prelude.ergo`) declares them with the methods the fixtures and the corpus call, provisionally until [#209](https://github.com/cevr/ergo/issues/209) fixes their method sets.
6. An interface is also a generic constraint, with Go's method-set rules.

## Methods

Status: **checked** (#196, 196m); the build refuses a direct method call until #50.

There are no receivers. A package function whose first parameter is a named type declared in the package is a function on that type, and `x.f(y)` calls `f(x, y)` (196m).

1. `x.f(a)` resolves to a field `f` of `x` first. Otherwise it resolves to the package function `f`, in the package that declares `x`'s type, whose first parameter's type is `x`'s type: `T`, `*T` or, for a generic `T`, `T[A, B]` whose arguments are distinct type parameters of `f`, which a call on an instance binds. A first parameter written as one of the function's own bracket parameters is that parameter, never a package type of the same name. The call checks the other arguments against `f`'s signature without its first parameter, and its rows are `f`'s rows, inferred or written.
2. `x.f` without a call is only a field (S104 when `x` has no field `f`): a function on a type has no method value. Written type arguments keep the call: `x.f[int](a)` is the call `x.f(a)` with the bracket parameters of `f` that its first parameter does not bind written in order (`g.pick[string]("x")` for `func pick[T, U](g G[T], u U) U`); `x.f[int]` alone is still S104. When `f` has no such bracket parameter, the brackets are no type arguments: `s.hs[0]()` is the S104 of `s.hs`, which suggests the call `s.hs()[0]`.
3. Several package functions may share a name when their first parameters are distinct named types declared in the package. Such a name is called as `x.f(…)`; a plain call `f(x)` with more than one candidate is S108 "several functions are named f; call it as x.f(…)". Lowering emits them as Go methods on the type, which Go allows. A first parameter that names a type declared twice (S108) counts as such a type when its type arguments are distinct type parameters of the function and the function is not `main`: which declaration the package keeps depends on their order, so the group's verdict never reads it; two functions whose first parameters name it still share it (check-v1 stage D1 rounds 3 and 4).
4. A named type satisfies an operation type when, for each operation, a function on the type exists (a field of the same name does not hide it: `x.Value` reads the field, and `func Value(c Counter) int` still provides `Value() int`) with the same remaining signature, no wider error row and no extra `needs` (S106, rows.md Wire 2). A function with type parameters beyond those its first parameter binds can be called as `x.f(…)`, but it cannot satisfy an operation, which takes no type parameters (S106, naming the parameter).
5. A first parameter `*T` needs an addressable `x` (S108), as Go's pointer receiver does ([Borrows](#borrows)).
6. An error type may not declare a function named `Error` on itself (S108), because the emitter generates it; an error type's text comes from its fields (fixture `error-method-on-error-type`).
7. The prelude's functions on slice and Map types, `Append`, `Get` and `Delete` ([stdlib.md](stdlib.md#v1-packages-task-scope-queue-and-slice), Slices and Maps), resolve by rule 1 from a value whose type is exactly an unnamed `[]E` or `Map[K, V]`, which binds their bracket parameters as a generic first parameter does; they are in no scope, so a package declares its own `Get` freely, and their bare call is S104 naming the dot form (check-v1 D-D6, D-D8). A defined type over a slice reaches `Append` after assignment ([Assignability](#assignability) clause 2): `let raw []int = xs`, then `raw.Append(x)`; a defined Map uses `let raw Map[string, int] = m`, then `raw.Get(k)` or `raw.Delete(k)`. `Info.Calls` records such a call, and every call `x.f(…)` of a generic `f`, with every type argument of `f` in its order, the ones `x` binds included, so the lowering writes `F[A, B](x, …)` (D-D8). *Prior art:* MoonBit declares `fn Array::push` and calls it `xs.push(x)`; Nim and D call any function by its first argument (UFCS); go/types records a method value's full instance in `Info.Instances`.
8. A call `x.f(…)` reaches the one function `x`'s type picks, for the order of row inference and for Go's initialization order, never every function named `f`: an initialization cycle through it names it by its type, `B.m`. *Prior art:* MoonBit resolves `x.f` by `x`'s inferred type in the type's package and allows same-named methods on different types; go/types resolves a selector with LookupFieldOrMethod and orders initialization by the method it resolved.

## Interface assignability

Status: **checked** (#196); runs where the prototype emitter can lower the program.

```go
type Store { Get() string }
type Mem {}

func Get(_ Mem) string { return "ok" }
func NewStore() Store  { return Mem{} } // Mem implements Store
```

1. A type `T` implements an interface type `I`, so a value of `T` is assignable to `I` ([Assignability](#assignability) clause 3), when `T`'s method set has every method of `I`, and for each such method:
   - the parameter types and the result type are identical to `I`'s;
   - the method is fallible exactly when `I`'s method is;
   - its error row is a subset of `I`'s method's error row;
   - its `needs` row is a subset of `I`'s method's `needs` row.

   This is method-row compatibility: an implementation may fail less and require less, never more. It is the same check `wire` applies to a provider (S106; [rows.md](rows.md#wire)). A value that holds a `Task` is not assignable to an operation type that holds none, which would erase its ownership (S108; [runtime.md](runtime.md#tasks) rule 3); a `Task` keeps its own type.
2. A union type is not an interface in this sense; it is assignable only by the variant rule (clause 4).
3. Resources keep their rules: assigning a resource to an interface without `Close` is an error (R22), and assigning it to one with `Close` moves it (R23) ([resources.md](resources.md#conversions)).
4. *Prior art:* Go satisfies interfaces implicitly by method set with identical signatures; Effect requires a service implementation's error and requirement types to fit the tag's; Java and Swift let an overriding method throw fewer checked errors, never more.

## Error types

Status: **checked** (#196).

1. A type named in any error row is an error type. It must be a named struct type, of this package or of an imported one such as `timeout.Expired` (S104). Inside a package, a struct type is an error type when an error row of that package, or of the package that declares the type, names it; a struct type that no such row names is an ordinary value type.
2. An error type is not an ordinary value: returning a value of an error type is a failure ([rows.md](rows.md#error-rows); fixture `error-as-value`, S101).
3. Error rows are sets of independent struct types, not one declared sum per function. *Prior art:* Gleam returns one declared `Result` error type per function; OCaml polymorphic variants and Koka effect rows are open sets; Ergo's rows are closed sets of nominal types with no open tail in v0.

## Operators

Status: **checked** (#196); the build refuses operators other than `==` and `+` until #50.

1. Go's operators on Go's operand types, with exact constant arithmetic (an overflow of the typed result is S108): arithmetic on numeric types, `+` on strings, ordering on numbers and strings, `==` and `!=` on comparable types (structs whose fields are comparable included; README Gap 26). Comparability is structural, and every use reads it (`==`, a tuple, a record field, a `comparable` constraint, a map key): a record or tuple compares when its fields do, a union only when every variant does, and a row value (`case err:`) and an operation type's value never do, because Go would compare them as interfaces and panic on a variant that does not compare (stage C round 2 review M1, M2); `&&`, `||` and `!` on `bool`, shifts on integers. S108 otherwise. A comparison is `bool`.
2. **One type per operator** (196u rule 4). Both operands of a binary operator have one type, and no operand converts by itself: `a + b` with `a int32` and `b int64` is S108 "mismatched types int32 and int64; write int64(a)". A literal takes the other operand's type (`x + 1` with `x int8` adds two `int8`s). Two literals each take the expected type, else their defaults, so `let d = 1 + 2.5` is S108 "mismatched types int and float64; write 1.0 + 2.5". *Prior art:* Rust's rule; Gleam and OCaml separate float operators.
3. **Shifts and conversions** (spec D-A3). A shift's left literal takes the expected type when that is an integer type, else `int`; any other expected type is S108 "the left operand of a shift is an integer, and float64 is expected here; write int(1) << n, then convert the result". The count is any integer type. A conversion `T(x)` gives a literal `x` no expected type: `x` is typed on its own, then converted, so `float64(1 << n)` converts an `int` shift (in Go it is invalid, a known trap). *Prior art:* Rust's `as` never types its operand from the target; Gleam and MoonBit shift through library functions on one integer type; Zig requires a fixed-width left operand unless the count is comptime-known.
4. `duration` takes `+`, `-`, ordering and `==` between durations; an integer literal is never a `duration` (S108). Its other arithmetic is [#175](https://github.com/cevr/ergo/issues/175)'s.
3. `&&` and `||` short-circuit (README Gap 10). `comparable` is a constraint; ordering constraints such as `cmp.Ordered` are [#211](https://github.com/cevr/ergo/issues/211)'s.

## Conversions

Status: **checked** by `internal/sketch/check` (check-v1 D-B7).

1. `T(x)` converts between numeric types by Go's rules (an integer or float to an integer or float, a complex to a complex, and a numeric constant to any numeric type that represents it), between `duration` and `int64`, and between a defined type and the type it is defined over, in both directions. Every assignment that makes no implicit conversion ([Assignability](#assignability) clauses 1, 2 and 5) is a conversion too, so a value converts to its own type, as go/types `convertibleTo` starts from `assignableTo`.
2. Nothing else converts (S108): not two defined types over one type (one hop is visible: `int(c)` of a `Celsius` over `float64` is S108 "convert through the type it is defined over: int(float64(c))"; a base with no name has none to call, so `Other(x)` of an `Ints` over `[]int` is S108 "go through the type it is defined over: let raw []int = x; then Other(raw)", under a name not bound there; each text checks when written back), not two records, not an integer to `string` (S108 "an integer does not convert to string; write \"\\(n)\" for its decimal text"), not a variant to its union or a value to an operation type (assignment does those: `let u Payment = p`).
3. *Principle:* nominal types, one visible hop. *Prior art:* Gleam and ReScript convert only through functions (`int.to_float`); Elixir uses functions; MoonBit uses `to_int64()` style methods; Go converts between any types with one underlying type, and `string(65)` is a vet warning.

## Function values

Status: rule 1 is **checked** (#196): a function value is assignable to a function type with identical parameters and results and rows that are subsets of the type's, when one of the two types has no name ([Assignability](#assignability) clause 5; S108 otherwise). Rules 2 to 5 are **designed, not yet implemented** ([#197](https://github.com/cevr/ergo/issues/197)).

1. A function type carries its parameters, result, error row and `needs` row. It carries requirements only, never purity ([rows.md](rows.md#footprints)).
2. A function-typed parameter is non-escaping unless its type says otherwise: the callee may call it and may hand it to a task that the call consumes, but it may not store, return or send it. A trailing block is such an argument. *Prior art:* Swift non-escaping closures by default; Effekt second-class blocks.
3. A `func` literal that escapes captures by copy and may not assign captured locals. A non-escaping inline block may assign them. A captured resource follows [resources.md](resources.md#closures).
4. **Concurrent parameters.** A function parameter that the callee hands to `go`, directly or through another call, is concurrent. The compiler infers this fact and records it in the exported interface summary. A block or literal passed to a concurrent parameter may not assign captured locals, so `parallel { n++ }` is an error. *Prior art:* Swift `@Sendable` parameters.
5. **Replayable parameters.** A transaction body ([stdlib.md](stdlib.md#sync-and-stm)) may not assign captured locals, use channels or consume a resource, and its footprint holds only `sync.Tx`.

## Borrows

Status: placement (rule 1, first sentence) is **checked** (#196): `*T` outside a parameter or receiver type is S108; rule 5 is checked (check-v1 stage D3b). The rest is **designed, not yet implemented** ([#197](https://github.com/cevr/ergo/issues/197)).

1. `*T` appears only on parameters and receivers. A borrow is never nil, stored, returned, sent or captured by `go`.
2. Two mutable borrows into one value must not overlap. With constant indexes the checker decides; with dynamic indexes it needs a dominating guard or it rejects the call. *Prior art:* Swift's law of exclusivity; Rust `get_many_mut`.
3. There are no pointer fields and no reference fields. Linked structures use recursive values behind unions, index arenas or `sync.Ref`. This is a loss against Go. *Prior art:* Rust keeps both reference fields, at the cost of lifetime parameters on the struct, and owned pointer fields (`Box`, `Rc`); Ergo gives up reference fields so that no signature needs a lifetime, and replaces written owned pointers with compiler-boxed recursion behind unions; Swift value types and Hylo's mutable value semantics make a similar trade.
4. Resources are lent by passing them as arguments ([resources.md](resources.md#lending)). A borrow and a lend obey the same no-escape rule.
5. An operation type is already a reference: its Go form is an interface or a runtime pointer. An operation called through a borrow of an operation type, `p.Close()` with `p *queue.Queue[int]`, is S108 "an operation type is already a reference; call it on the value" at the receiver, for a std operation type and the program's own alike; call it on the value, `q.Close()` (checked, check-v1 stage D3b). *Prior art:* Go calls a method of an interface only on the interface value, never through `*I`; Gleam, Elixir and ReScript have no pointer to a module's abstract type.

## Generics

Status: the spelling is decided on [#196](https://github.com/cevr/ergo/issues/196) (196g as amended, sixth round). The 196a checker (`internal/sketch/check`) checks the earlier implicit spelling, in which an undeclared one-letter name is a parameter; check-v1 brings it to this spelling. The build refuses generics until [#50](https://github.com/cevr/ergo/issues/50).

```go
func Transform[A, B, E, R](xs []A, limit int64, f func(A) B fails E needs R) []B fails E needs R {
	// designed
}
```

1. Type parameters are declared in brackets after the name, `[A, B, E, R]`; a bare upper-case letter is not a parameter by itself. The kinds stay implicit: a parameter may range over types (`A`, `B`), error rows (`E`, after `fails`) or capability rows (`R`, after `needs`). `[T any]` is written `[T]`.
2. A constraint follows its parameter inline, `[T ordered]`: a method set, or one of the predeclared `comparable`, `ordered` and `number`. There is no `where` clause. Method constraints resolve through the type's owner package, with one answer per type.
3. Rows in a generic signature may be elided only where they pass through unchanged. A declared requirement is never elided.
4. Exported signatures spell their rows and type parameters, so clients check against interface summaries, Go's export-data model.
5. **Lowering.** Generics emit as Go generics, which Go compiles by GC shape. Specialised copies need a measured win under [#190](https://github.com/cevr/ergo/issues/190). An instantiation cycle, a generic type or function that instantiates itself with a growing argument (`func f[T]() { f[[]T]() }`), is S108, as Go rejects it: its instances never end. A method's receiver parameters count as the type's own, as `go/types` canonicalises them, so `func (r R[T]) M() { R[[]T]{}.M() }` is a cycle too. Signatures count like bodies: a parameter, a result, a bodiless declaration, an interface-typed parameter or a constraint that instantiates with a growing argument (`func (R[T]) M(x R[[]T])`), through a renamed receiver parameter or across two receiver types, is a cycle as well. The checker finds it on the instantiation graph, not by a depth limit, and before rows are inferred, so rows iterate over a finite set. *Prior art:* Go GC-shape stenciling; MoonBit and Rust monomorphise; Ergo's earlier measurements refused frontend monomorphisation ([design.md](../design.md#compile-speed-is-a-language-constraint)).
6. **Inference.** Type arguments are inferred by unification over the value arguments with Go's unifier (`go/types` unify.go), never from a call's expected result. Inference is sound and a documented subset of Go's (D6 as amended in rounds 7 and 8): when Ergo infers, Go infers the same types for the same call after row erasure. The subset is typed value arguments, structural unification, interfaces through method sets, conflicts between arguments, literals (196u rule 6: a literal argument binds a parameter only when no typed argument binds it, and a parameter that only literals bind takes their common default, so `Two(1, 2)` infers `int` and `Two(x, 2)` with `x int8` infers `int8`; mixed defaults, `Two(1, 'a')`, are S108 "cannot infer T; write the type arguments"), and written type arguments. Every typed argument unifies first, in source order, then the literals default, and each literal is then checked with its parameter's type expected. A function literal is a typed argument, since its parameter and result types are written (inferring unwritten ones is #214); only its unwritten rows take the parameter type's, so `Apply(func(v int8) int8 { return v }, 1)` and `Rev(1, func(v int8) int8 { return v })` infer `int8`. A trailing block writes no types and binds what is left after the literals. Outside the subset, the call is S108 "cannot infer T; write the type arguments, e.g. Fn[int](…)", its only diagnostic: a parameter only a method or core-type constraint determines (`Use(x)` for `Use[T comparable, P M[T]]`, where `type M[T] { M(T) }`), and a generic function passed as an argument without all its type arguments (`Apply(Id, 1)`, `Fix(Id)`). The written forms stay valid: `Use[int](x)`, `Apply(Id[int], 1)`; a constraint error stays a constraint error when typed or written arguments fix every type argument. A call whose inference fails or is refused reports one diagnostic, and its result is invalid, so nothing cascades from it. An argument that is invalid, or is itself a failed or refused call, fails the enclosing generic call with no diagnostic of its own (one rule, in the inference loop; round 9), so `Id(Apply(Id, 1))` reports one refusal; independent errors in other arguments still report. Each typed argument is assigned to its parameter: the match may be inexact at the top (an interface on either side matches a type that has its methods, by unifying the method signatures, so a wider interface, a named generic interface, a concrete type or a pointer infers through its method set), and element types match exactly. A parameter bound by an earlier argument must match every later one: a conflict is S108 at the argument (`Two(x, y)` with `x` an interface and `y` a struct; of an interface and a non-interface neither is chosen, so the order of the arguments never decides), while `Two[I](x, y)` for an operation type `I { M() }`, with every type argument written, infers nothing and only assigns. Ergo departs from Go where its types do: a named type unifies with a type literal only when it is an interface (types are nominal), and a named union unifies with the variant its argument names. A generic function inside parentheses is a value, as in Go: `(Fn[int])` is the instance, and `(Fn)[int]` or `(Fn)(1)` is S108. In `Info`, an explicit instantiation `Fn[int]` and the parentheses around it carry the instance type while the name `Fn` keeps its generic type; in an inferred call `Fn(1)` the name carries the instance, as in `go/types`. Every literal carries its one type in `Info`, recorded when it is checked ([Basic types](#basic-types) 6; 196u rule 7): no expression's type is updated afterwards. A row parameter binds to the row of a function-typed argument, less the row's written part; `fails E | Expired` is the union. When several arguments bind one row parameter, it binds to the union of their rows: rows are finite sets, and a function with a narrower row is assignable where a wider one is expected, so the union is the least binding that admits every argument. A written type argument is not widened. A composite literal of a generic type, or a generic variant in a `case`, written without type arguments takes them from the expected type, else from its field values (`Some{n}`, `Enter{x}`). A type parameter left unbound is S108; a row parameter left unbound is empty. A result-position type parameter bound by a block with no value stands for no value, lowered to `struct{}`. *Prior art:* Go `types2/infer.go`; Koka infers effect rows; OCaml and ReScript disambiguate constructors by the expected type; Gleam's `Nil` and Rust's `()` for no value.

## Loops and len

Status: **checked** by `internal/sketch/check` and **lowered** by `internal/sketch/lower`, with `ergo run` tests (check-v1 stage D2: D-D4, D-D5).

1. **Domains** (D-D4). `for x in e` binds the element and `for i, x in e` the position and the element, over four domains: a slice `[]T` (position `int`); a `Map[K, V]` (one name binds the value, two the key and the value; order unspecified); the prelude's `Range[T]` (one name, counting from `Start` up to `End`, `End` excluded); and the prelude's `Seq[T]` (one name). A second name over a `Range` or a `Seq` is S108 "a for over Range[int] binds one name, the element: for x in …". Any other domain is S108 with a message that names the Ergo form: a string ("count its bytes with for i in 0..len(s)"), an integer ("count with for i in 0..n"), a pointer ("write for x in *p"), anything else ("a for takes a slice, a Map, a Range or a Seq"). A `Range` over a non-integer `T` is S108. A refused domain or name still binds its names, with no type, so nothing cascades: it adds no S110, and `for Some in xs` is one diagnostic. A `for` over a `Map` that the body changes follows Go: it visits each entry present at the start at most once, in unspecified order; an entry inserted during the loop may or may not be visited; an entry deleted before it is reached is not visited. There is no snapshot, which would allocate on every loop. *Prior art:* Go's range over a map has these rules; Rust's borrow checker refuses a change during the loop; Gleam's and Elixir's maps are immutable values, so a loop sees the map it started with.
2. **`a..b`** has the type `Range[T]` for one integer type `T`, the operands' (D-A1 types the literals), and works outside a `for`: `let r = 0..n`, `r.End - r.Start`. Other operand types are S108. *Prior art:* Rust's `0..n` is a `Range` value; MoonBit's `0..<n`.
3. **Bindings** are immutable and read: an unread one is S110, `_` ignores it. A single name follows the one binder rule (check-v1 D-D2 and D-D3; [rows.md](rows.md#totality), Totality 6): a known constant, type or capability name is a pattern, so `for T in ts` tests the type; a binding may be any irrefutable pattern. `for cond { }` needs a `bool`.
4. **`Seq[T]`** is `type Seq[T] func(yield func(v T) bool)`, a protocol any function implements by conversion, `Seq[int](f)`. `for v in s` runs the body as `yield`; a `break` or `return` makes `yield` return false. *Law:* a Seq never calls `yield` again after it returns false. `queue.Values()` returns one ([stdlib.md](stdlib.md#v1-packages-task-scope-queue-and-slice)). *Prior art:* Go's `iter.Seq[T]` and range over func; Rust's `IntoIterator`.
5. **Map entries.** A write `m[k] = v` inserts or replaces the entry, and needs `m` rooted at a `let mut`. A read `m[k]` is S108 "a Map read may miss; write m.Get(k), which returns an Option", whatever `V` is: there is no default value for a missing key, and `m[k] op= v` reads. The default of a `Map` is an empty, writable `Map`, wherever a default is made: a `let` with a `Map` type and no value, a field a record literal omits, and a `Map` inside a defaulted record or tuple, named or not, a generic instance included, at any depth. The lowering writes `map[K]V{}`, never Go's nil map. A defined type or instance whose default holds a `Map` has one generated Go function that builds its default, which a nested default calls, so the lowering's work and output grow linearly in the types; every default builds its own `Map`s, so two defaults never share one. *Prior art:* Rust's `#[derive(Default)]` emits one `default()` per type that calls each field's; Rust's `map[k]` panics on a missing key; Gleam's `dict.get` returns a `Result`; Go returns the zero value, the habit this rule removes, and its nil map panics on a write; Rust's `HashMap::new()`, Gleam's `dict.new()` and Elixir's `%{}` are empty and usable.
6. **`len` is the only built-in function** (D-D5). It takes a slice, a `Map` or a `string` (its bytes); a pointer is S108 "len takes a slice, a Map or a string", so write `len(*p)`. Every other Go built-in is S104 with the Ergo form ([diagnostics.md](diagnostics.md#s104-unknown-name)). An index does not see through a pointer either: `(*p)[i]`.
7. **Lowering.** A slice and a `Map` are Go's `range`; a `Range` is a counting loop over its value, evaluated once; a `Seq` keeps the one Go shape of every Ergo function value and is adapted at the loop to Go's range over func, so a `break` is Go's. Each binding is a Go variable of one iteration, so a function literal captures that iteration's value.

## Prelude types

Status: **declared** in the embedded prelude (`internal/sketch/std/prelude.ergo`) and checked by the checker (#196); the build refuses them until #50, except the timeout result `timeout.Expired`. The capabilities' method sets are provisional until [#209](https://github.com/cevr/ergo/issues/209); `Option` is [#99](https://github.com/cevr/ergo/issues/99)'s.

- `Option[T]` with `Some` and `None` ([#99](https://github.com/cevr/ergo/issues/99)).
- `Range[T] { Start T, End T }`, the value of `a..b`, and `Seq[T]`, an iterator ([Loops and len](#loops-and-len)).
- `Task[T, E]`, a resource ([runtime.md](runtime.md#tasks)).
- `Decision[S, O]` with `Enter`, `Stay`, `Ignore`, `Reject` and `Done` ([runtime.md](runtime.md#machines-and-actors)).
- The capabilities `Clock`, `Scheduler`, `Random` and `Console`.

## Laws

Status: **designed, not yet implemented** ([#125](https://github.com/cevr/ergo/issues/125), [#170](https://github.com/cevr/ergo/issues/170)).

```go
type Codec[T] {
	Encode(v T) string,
	Decode(s string) T fails Malformed,

	law RoundTrip(v T) { try Decode(Encode(v)) == v },
}

type Store {
	Find(id string) Order fails NotFound,
	Put(id string, o Order),

	law FindAfterPut(id string, o Order) { Put(id, o); try Find(id) == o },
}
```

1. A law is `law X(params) { … }`, a member of an operation type beside its operations (196l). Its body ends in a `bool` expression, and a failure that escapes the body refutes the law. `ergo test` checks it like a fuzz target, with generated arguments.
2. Every law is a provider law: `ergo test` checks it against every provider of its type, which the closed world lists.
3. A refuted law refuses the build. A law that passes a finite suite is reported as tested, never as proved (NORTH_STAR pillar 4).
4. Laws are declared in their type, not found by name; the `LawX` naming rule is gone. The same form serves every client: codecs, providers, schedules and machines. Until laws are implemented, the prototype refuses a law with S109 ([#162](https://github.com/cevr/ergo/issues/162)). *Prior art:* Lean type classes carry their laws as members; Haskell states class laws beside the class and checks them with QuickCheck; Elixir `StreamData` properties.

## Codecs

Status: **designed, not yet implemented** ([#49](https://github.com/cevr/ergo/issues/49)).

1. A type's wire form is found by name: `ParseX` decodes into `X`, and `x.Wire()` encodes. Go's `netip.ParseAddr` and `String` pair is the model.
2. A derived codec's rows are the union of its fields' rows. `json.Decode[Order]` has the error and `needs` rows of every `ParseX` it reaches.
3. README Gap 20 records the open question about discovery by name.

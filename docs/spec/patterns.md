# Derived types and patterns

Status: **designed**: no compiler support yet. The parser refuses `derive` after the name of a type declaration with S001.

A declaration `type T derive E` evaluates `E`, a closed [type plan](../../GLOSSARY.md#constructs-contracts-and-laws), at check time and derives a bundle of ordinary declarations that `T` owns: the type `T`, the types it needs, a parse function, its failure, and laws. A plan is built by predeclared constructors and by ordinary functions of any package, the user's included. One plan is a byte pattern with named captures: an optional capture gives an `Option`, a repeated capture gives a slice of every occurrence, and an alternation of named captures gives a union that `match` covers exhaustively. The compiler checks the pattern at check time and lowers it to a matcher that it builds and checks at compile time; no pattern is parsed or compiled when the program runs. The owner put pattern declarations, this one derivation rule and its byte grammar in v1 ([decisions](../decisions.md#owner-direction-2026-11-11)).

Each rule below names the test that holds it. These tests do not exist yet; the [status ledger](status.json) lists none until they do. The examples are fenced as `ergo`, not `go`, because the parser does not accept `derive` yet, and every `go` fence of the specification must parse; the parser's change turns them to `go`.

```ergo
// package formats: a plan function, ordinary code of a user package
func DateShape() TypePlan {
	Pattern(`(?<Year>[0-9]{4})-(?<Month>[0-9]{2})(-(?<Day>[0-9]{2}))?`)
}

// package app
import app/formats

type Date derive formats.DateShape()
// derives:
//   type Date { Year string, Month string, Day Option[string] }
//   func ParseDate(s string) Date fails NoMatch
//   func FindDate(s string, from int) Option[(Date, int, int)]

type Token derive Pattern(`(?<Word>[A-Za-z]+)|(?<Number>[0-9]+)`)
// derives: type Token = Word { Value string } | Number { Value string }

func describe(text string) string fails NoMatch {
	match try ParseToken(text) {
	case Word as w: "word \(w.Value)"
	case Number as n: "number \(n.Value)"
	}
}

type Port derive Refine(Of[int](), 1, 65535) // the bundle of: type Port int where 1 <= it && it <= 65535
```

## Derivation

1. **Syntax.** `type T derive E` is a type declaration whose body is the expression `E`. `derive` is a contextual word, not a keyword: it has its meaning only after the name of a type declaration and before a token that starts an expression on the same line. Everywhere else `derive` is an identifier, so a type, a package or a local named `derive` keeps its meaning, and `type T derive` at the end of a line declares `T` over a type named `derive`. A derived type has no type parameters, and `derive` does not combine with `where` or with a body; each is S001 at `derive`. Test: the parser's grammar property (`internal/sketch/syntax`); the text of each refusal is a row of `TestFixtures` (`internal/sketch/check`).
2. **The plan.** `E` must have the type `TypePlan` ([Type plans](#type-plans)) and be a plan expression: a call of a constructor or of a plan function ([Plan functions](#plan-functions)) whose arguments are constants or plan expressions. The checker evaluates `E` once, at check time, under the budget of [Budgets](#budgets) 1. Any other expression is S108 at `E`: "the derive of Date needs a TypePlan built from constants; f reads a value only known at run time". Test: `TestDeriveModel` (`internal/sketch/check`).
3. **The bundle.** The value of `E` determines the declarations that `T` owns: the type `T` itself, the types its shape needs ([Capture shapes](#capture-shapes) 5, [Type plans](#type-plans) 4), and the functions and laws of its node ([The pattern bundle](#the-pattern-bundle), [Coherence](#coherence-with-refinements-and-codecs)). The parser places the derived declarations after `T` and marks them derived, as it does for a refinement; the formatter prints `type T derive E` as written and does not print what it derives. A derived declaration is an ordinary declaration of `T`'s package: a call, a `match`, a codec, the checker and `ergo inspect` find it by name as they find a written one. Test: `TestDeriveModel`; the formatter's idempotence property (`internal/sketch/format`).
4. **Names.** A derived name follows the visibility rule of every name: an upper-case `T` gives `ParseT` and `FindT`, a lower-case `t` gives `parseT` and `findT` ([Refinements](types.md#refinements) 4). Capture and field names are used as written, never capitalised or renamed. A derived name that another declaration of the package already has, written or derived, is S108 at the derive, naming both: "DateEntry is already declared in this package: the derive of Date generates it". Test: `TestDeriveModel`; the text is a row of `TestFixtures`.
5. **Order and cycles.** The checker evaluates each derive after the plan functions it calls are checked, and before any declaration that uses a derived name. A plan that needs the shape of a derived type (`Refine(Of[A]())` with `A` derived) evaluates `A` first. A cycle through derives, plan functions and the shapes they need is S108 at the first derive of the cycle, naming each step. A plan that names a type only as a field's type (`List(Of[Tree]())` inside the plan of `Tree`) needs no shape and makes no cycle, as a written recursive record does not. Test: `TestDeriveModel`.
6. **No other door.** Only a `type … derive` declaration turns a plan into types. A call of a plan function anywhere else returns a `TypePlan` value and never a type: no result type of any function depends on an argument's value. The derivation keys on the plan's value, never on the name of the function or package that built it: a std function and a user function that build equal plans derive equal bundles. Test: `TestDeriveModel` (its generated programs build each plan both directly and through wrapper functions in two packages, and compare the bundles).
7. **Inspection.** `ergo inspect` shows, for each derived type, the plan it evaluated, the declarations it derived, and for a pattern its grammar profile (bytes), its matching (whole text for a parse, leftmost for a find), the selected engine, the reason a faster engine was not selected, and the counts of [Budgets](#budgets) 4. Test: the inspect text is a row of `TestInspectAPIRoute` (`cmd/ergo`).

## Type plans

1. **The algebra.** `TypePlan` is a predeclared, closed type: a value is one of the nodes below and nothing else. It has no variants a program can name, no fields and no operations. A program builds it only with the constructors below and reads it only through a derive. Test: `TestDeriveModel`.

   | Constructor | Result shape | Input it parses |
   | --- | --- | --- |
   | `Of[X]()` | the named or basic type `X` itself | as `X` does |
   | `Refine(base TypePlan, lo int, hi int)` | a defined type over the base, refined to one closed span | the base value |
   | `Record(fields []FieldPlan)` | a record type, fields in order | none: codecs only |
   | `Union(variants []FieldPlan)` | a union type, one variant per entry | none: codecs only |
   | `Optional(p TypePlan)` | `Option` of `p`'s shape | none: codecs only |
   | `List(p TypePlan)` | a slice of `p`'s shape | none: codecs only |
   | `Pattern(text string)` | a record or union from the captures of `text` | a `string`, whole |

   `Field(name string, p TypePlan) FieldPlan` names one field of a `Record` or one variant of a `Union`. `FieldPlan` is the second predeclared plan type, as closed as `TypePlan`.
2. **Predeclared and shadowable.** `TypePlan`, `FieldPlan`, the eight constructors and the failure `NoMatch` ([The pattern bundle](#the-pattern-bundle) 2) are predeclared in the universe block, as `wire` is ([Codecs](types.md#codecs) 1), not names of a std package. A declaration of the same name in a package, an import of that name, a parameter or a local hides the predeclared name in its scope, and inside that scope the name means the declaration: inside std `json`, `json`'s own name is meant. A hidden constructor is not reachable under another spelling in that scope, so a package that declares its own `List` builds its plans in another package. The derivation never treats a shadowing declaration as the constructor. Test: `TestDeriveModel`.
3. **Each node fixes its shape and its input.** `Pattern` parses a `string`; `Refine` parses its base's value; `Of[X]` parses what `X` parses. A structural node (`Record`, `Union`, `Optional`, `List`) parses no text: the derived type gets the codecs the [codec rule](types.md#codecs) gives a written type of that structure, and no guessed delimited-text parser. Test: `TestDeriveModel`.
4. **Nested types.** A node inside a `Record`, a `List` or an `Optional` that derives a nominal type of its own (a `Record`, a `Union`, a `Refine` or a `Pattern`) is named after its owner and its field: the field `Port` of `Server` gets the type `ServerPort`. A variant of a `Union` is a record type named by the variant, as an inline variant of a written union is ([Unions](types.md#unions)); its plan is a `Record`, or `Record([]FieldPlan{})` for a variant with no fields. A field name or a variant name that is not an identifier, or a name used twice in one `Record` or `Union`, is S108 at the literal that spelled it. Test: `TestDeriveModel`.
5. **Refine.** `Refine(base, lo, hi)` requires a base whose shape has an integer underlying type, a `string` or a slice; for a `string` or a slice the span bounds its length. `lo > hi` is the empty set, S108 as in [Refinements](types.md#refinements) 2. A `Refine` over a refined base is S108, as a refined base is. Test: `TestDeriveModel`.

## Plan functions

1. **What a plan function is.** A plan function is a function whose result type is `TypePlan` or `FieldPlan`. It has no `fails` and no `needs` row, no `extern` and no type parameters: a plan over another type takes that type's plan as a parameter, `Paged(Of[User]())`. Its parameters are constants of a basic type, `TypePlan`, `FieldPlan`, or slices of them. Its body may use `let`, `if`, `match`, `for`, string interpolation and concatenation, integer arithmetic, and calls of constructors and plan functions; any other call, an `import go` name, a capability or a task is S108 at that call: "a plan function runs at check time; Console.Println charges Console". Test: `TestDeriveModel`.
2. **Ordinary and exported.** A plan function is an ordinary function of its package, exported by an upper-case name. A user package may declare plan functions for its grammars, parameterised fixed-width identifiers or composed record schemas, and another package derives from them. A package cannot register syntax, a solver, a constructor or a hook that computes types: the algebra of [Type plans](#type-plans) 1 is closed. Test: `TestDeriveModel`.
3. **Check time only.** A plan function and a `TypePlan` value exist only at check time. A use of a plan function or of a `TypePlan` value outside a plan function's body and outside a derive is S108: "a TypePlan exists at check time only; use it in a type … derive declaration". The run build and the test build emit no Go for a plan function. Test: `TestDeriveModel`; the run build of a program equals, byte for byte, the run build of the same program with its plan functions removed and each derive replaced by its bundle written out.
4. **Across packages.** The summary of a package holds the checked body of each exported plan function. A change of that body changes the summary, so each package that derives from it is checked again. Test: `TestDeriveModel` (its two-package programs change a plan function and observe the dependent bundle change).

## Budgets

1. **Plan evaluation.** The evaluation of one derive stops, as S108 at the derive, when it exceeds any of: 100,000 evaluation steps (one step per expression node evaluated), a call depth of 64, 65,536 bytes of strings built, or 4,096 plan nodes. The message names the limit, the count reached and the plan function that was running: "the derive of Log stopped at 100000 evaluation steps in formats.Repeat; a plan is built in bounded steps". Evaluation never calls Go, does no I/O, reads no clock and uses no reflection. Test: `TestDeriveModel` (it reaches each limit and one below each).
2. **Pattern construction.** A pattern is refused, as S108 at the pattern's byte where the limit was crossed, when it expands to more than 4,096 nodes or 65,536 NFA instructions, or nests deeper than 256. A bounded repeat counts its expansion before it is built, so `a{1000}{1000}` is refused without building a million nodes. Test: rows of `TestFixtures`; `TestBudgetsAndIdentity` (`internal/sketch/regexpplan`).
3. **Matcher selection.** Building a deterministic matcher is attempted within 4,096 states and 1,000,000 counted steps of closure, edge and tag work. Direct Go is used when its emitted source is at most 8,192 bytes; else a table of at most 65,536 encoded bytes; else the canonical NFA. A limit reached here selects the next engine and is never an error, since the NFA is always built ([Lowering](#lowering) 1). Test: `TestBudgetsAndIdentity`.
4. **Visible.** `ergo inspect` shows each limit, the count each derive reached and the selection reason. A limit may change between compiler versions; the language of a pattern and the value a parse returns never depend on a limit, only the engine and the build cost do. Test: the inspect text is a row of `TestInspectAPIRoute` (`cmd/ergo`).

## Pattern grammar

1. **Bytes.** A pattern is matched against the bytes of a `string`, one byte at a time. The pattern text is printable ASCII; any other byte in the text is S108 at that byte: "a pattern is ASCII; write the byte as \xC3". Test: `TestPatternGrammar` (`internal/sketch/derive`).
2. **Atoms.** A literal byte; `.`, any byte but `\n`; an escaped metacharacter (`\.`, `\\`, `\(`, `\[`, `\{`, `\|`, `\*`, `\+`, `\?`, `\^`, `\$`, `\-`, `\]`, `\}`, `\)`); `\n`, `\r`, `\t`, `\f`, `\v`; `\xHH`, one byte; the classes `\d` (`[0-9]`), `\w` (`[0-9A-Za-z_]`), `\s` (`[\t\n\f\r ]`) and their complements `\D`, `\W`, `\S` over all 256 bytes; a bracket class `[…]` of bytes, ranges `a-z` and these escapes, complemented over all 256 bytes by a leading `^`. A range whose low end is above its high end, an empty class, or a class that holds no byte after complement is S108 at the class. These meanings equal Go's `regexp` on ASCII input. Test: `TestPatternGrammar`.
3. **Operators.** Concatenation; alternation `|`, whose left branch has priority; the greedy repeats `*`, `+`, `?`, `{n}`, `{n,}` and `{n,m}`, with `n` and `m` at most 1000 and `n <= m`; a group `( … )`, which captures nothing; and a named capture `(?<Name> … )`, `Name` an identifier. Test: `TestPatternGrammar`.
4. **Refused, with the form to write.** Each is S108 at its first byte, and the message names the replacement: a lazy or possessive repeat (`*?`, `+?`, `*+`); a backreference (`\1`, `\k<…>`); a lookaround (`(?=`, `(?!`, `(?<=`, `(?<!`); a flag group (`(?i)`) or a non-capturing group spelled `(?:` ("write ( … ): a group captures only with a name"); `(?P<`; an anchor (`^`, `$`, `\A`, `\z`, `\b`, `\B`: "a parse matches the whole text and a find the leftmost match; drop ^"); a Unicode class (`\p{…}`); a POSIX class (`[[:alpha:]]`); any other escape. Test: `TestPatternGrammar`; the texts are rows of `TestFixtures`.
5. **Languages the engine refuses.** A pattern whose language is empty (`[^\x00-\xff]`) and a repeat whose body matches the empty text (`(a?)*`, `(?<X>)+`) are S108 at the repeat: "the body of this repeat can match no bytes; each repetition must consume a byte". Test: `TestPatternGrammar`; `TestBudgetsAndIdentity`.
6. **Matching.** A parse matches the whole input. A find returns the match that starts first; among the matches at that start, the one that branch priority and greedy repeats select, the same one Go's `regexp` selects on ASCII input with the pattern anchored at its start. The match is the same for every engine ([Lowering](#lowering) 1). Test: `TestTaggedConstructionAgrees` (`internal/sketch/regexpplan`).

## Capture shapes

1. **The scope of a capture.** The scope of a named capture is the nearest named capture that encloses it, or the whole pattern. Each capture name is used once in a pattern; a second use is S108 at the second, naming the first. Test: `TestPatternShapes` (`internal/sketch/derive`).
2. **Field type by participation.** In each successful match of its scope, a capture takes part some number of times. A capture that takes part exactly once in every match of its scope gives the field type `S`; at most once, and not in every match, gives `Option[S]`; possibly more than once (under a repeat whose maximum is above one, inside its scope) gives `[]S`, which holds every occurrence in input order. `S` is `string` for a capture with no named capture inside it, and the record of rule 5 otherwise. Thus `(?<X>a+)` gives `X string` and `(?<X>a)+` gives `X []string`. Test: `TestPatternShapes`: its generated patterns compare the derived shape with a reference that counts participation on the pattern's tree, and every generated match with the reference matcher's capture tree.
3. **Absent is not empty.** A capture that does not take part is `None`, and one that takes part over no bytes is `Some("")`; a repeated capture that never takes part is the empty slice, a slice with no elements. A repeated capture's elements are its occurrences, each with its own text; the last occurrence alone is not a field, and `slices.Last(v.X)` names it. Test: `TestPatternShapes`.
4. **Repeated groups keep their records.** A repeat whose body holds two or more named captures of the same scope, and is not itself inside a named capture in that body, is S108 at the repeat: "name the repeated group, (?<Entry>…)*, so each occurrence is one record". So a repeat yields one slice of records and never parallel slices that lose which key went with which value. Test: `TestPatternShapes`; the text is a row of `TestFixtures`.
5. **Nested records.** A named capture `X` with named captures inside it gives a record type named `<Owner>X`, `Owner` the type whose field it is, with one field per inner capture of its scope, in pattern order. The capture's own text is not a field. Its participation in its scope gives `<Owner>X`, `Option[<Owner>X]` or `[]<Owner>X`, as rule 2 gives for a string. Test: `TestPatternShapes`.
6. **The record form.** A pattern whose top level is not the union form of rule 7 gives a record `T` with one field per capture of the whole-pattern scope, in pattern order. A pattern with no named capture gives `T {}` and its parse only accepts or refuses. Test: `TestPatternShapes`.
7. **The union form.** A pattern whose top level is an alternation in which every branch is exactly one named capture gives a union `T` with one variant per branch, named by the capture, in branch order. A variant whose capture has no named capture inside it is `Name { Value string }`; otherwise its fields are rule 5's. A top-level alternation in which some branch is one named capture and another is not is S108 at the first unnamed branch: "every branch of Token is a named capture, or none is: name this branch, or group the alternation as ( … ) for a record". Grouping the alternation, `((?<A>x)|(?<B>y))`, gives the record `{ A Option[string], B Option[string] }`. Test: `TestPatternShapes`.
8. **Equality.** Two values of a derived type are equal when their fields are equal, slice elements included, by the type's `==` where it is comparable and by the structural equality that laws use otherwise. Test: `TestPatternShapes`.

## The pattern bundle

1. **Parse.** `type T derive Pattern(p)` derives `func ParseT(s string) T fails NoMatch`. It succeeds when `s` as a whole is in the language of `p`, with the value that [Pattern grammar](#pattern-grammar) 6 selects and [Capture shapes](#capture-shapes) projects. Test: `TestPatternShapes`; `TestPatternLoweringAgrees` (`internal/sketch/lower`).
2. **The failure.** The prelude's `NoMatch { Type string, At int }` is the one failure. `Type` is the derived type's name; `At` is the length of the longest prefix of `s` that some word of the language starts with, so `At` is the byte at which no match can continue, or `len(s)` when the text ends too early. `At` is the same for every engine. Test: `TestPatternShapes`.
3. **Find.** `func FindT(s string, from int) Option[(T, int, int)]` returns the match of [Pattern grammar](#pattern-grammar) 6 that starts at or after `from`, with its start and end as byte positions of `s`. A `from` below 0 or above `len(s)` gives `None`. A find keeps no state between calls: a caller iterates by calling it again from the end of the last match, or from one byte later after an empty match. Test: `TestPatternShapes`.
4. **Substrings.** A captured `string` is a substring of `s` and shares its bytes, as Go's slicing does; holding a capture keeps all of `s` alive. Test: `TestPatternLoweringAgrees`.
5. **Laws.** The derivation adds two laws, which `ergo test` runs with the package's other laws:
   - `TMatches(s string)`: `ParseT(s)` and `FindT(s, 0)` agree with the reference matcher ([Lowering](#lowering) 6): the same success or failure, the same `At`, the same spans, and equal values by [Capture shapes](#capture-shapes) 8. Half its cases draw `s` from the witness generator of rule 6 and half draw arbitrary bytes.
   - `TAccepts(w string)`: each witness `w` of rule 6 parses.

   Both are reported as tested, by the one rule of [Laws](types.md#laws) 10: the input domain is all strings, so they are never proved. Test: `TestPatternLoweringAgrees`.
6. **Witnesses.** The witness generator walks the pattern's tree: it picks a branch of an alternation, a byte of a class, and a count of a repeat, within a size that grows with the case's size, so each word of the language is reached at a large enough size. A generated word need not be matched by the branch that generated it: the reference decides the value. The generator reaches an absent and a present optional capture, and a repeat of zero, one and several occurrences. A failing case prints the seed, the pattern, the text, both values and the command that replays it. Test: `TestPatternShapes` (each shape branch reached, counted by the generator).

## Evidence

1. **The compiler's checks decide the matcher.** Evaluating the plan, deriving the shape, and building and checking the matcher ([Lowering](#lowering) 1 and 2) are checks of the compiler. They alone decide which engine is emitted. Test: `TestConstructionRejectsCorruption` (`internal/sketch/regexpplan`).
2. **The laws decide nothing.** `TMatches` and `TAccepts` are tested regression evidence. A law that passed never selects an engine, never removes a check, never stands as the evidence of a fact, and never admits a rewrite; a refuted law fails `ergo test` as any law does. No input of the compiler's matcher construction is a law result. Test: `TestConstructionRejectsCorruption`: a corrupt candidate is refused even when a law is forced to report that it passed.
3. **No proof claim beyond its domain.** A finite list of every case proves a statement only about that list. The construction argument ([Lowering](#lowering) 2) is the compiler's elaboration contract, as the lowering of `match` is: it is checked in full for each pattern, and it is not a machine proof about the Go compiler. Test: `TestTaggedConstructionAgrees`.

## Diagnostics

1. **Inside the literal.** An error in a pattern (a bad escape, range, class, repeat bound or capture name, a duplicate name, a refused form or a crossed limit) is S108 at the byte of the pattern that causes it, with that byte's line and column in the source file. The checker maps the decoded byte back through the escapes of an interpreted string and through the lines of a raw string. Test: rows of `TestFixtures`, one for each form; `TestPatternDiagnostics` (`internal/sketch/check`): generated patterns with one error at a chosen byte, spelled as raw, interpreted and concatenated strings, give the error at that byte's source position.
2. **Built text.** A pattern text built from pieces (a concatenation, an interpolation, a parameter of a plan function, a constant of another package) keeps, for each byte, the position of the literal that spelled it. The error is at that position. When it is in another package, or in a plan function's body, the diagnostic adds the derive as related information ("derived at app/date.ergo:4:6"). Test: `TestPatternDiagnostics`.
3. **One mistake, one diagnostic.** A refused derive declares `T` as a refused type: a use of `T`, `ParseT` or `FindT` adds no diagnostic ([diagnostics](diagnostics.md)). The checker never replaces a refused pattern by `any`, a map of captures, a wider type or a run-time panic. Test: `TestPatternDiagnostics`.

## Lowering

1. **Built at compile time.** The compiler turns a pattern into one canonical ordered tagged NFA, the reference. It then tries a deterministic matcher that follows the same transitions; when the construction check (rule 2) holds within [Budgets](#budgets) 3, it emits direct Go control flow or a table of the same edges, and otherwise the canonical NFA. A capture is recorded as ordered open and close events, so every occurrence of a repeated capture and the nesting of its records are kept. No pattern is parsed or compiled at run time, and no matcher has a mutable package variable or scratch shared between calls. Test: `TestTaggedConstructionAgrees`, `TestEmittedDriversAgree` (`internal/sketch/regexpplan`).
2. **The construction check.** Each tree form (empty, bytes, concatenation, alternation, capture, repeat) builds NFA paths that keep the language, the branch priority and the whole event tree. A deterministic candidate is used only when a check over each reachable state, all 256 bytes and the end of input shows that each edge follows exactly one NFA path with the same ordered events; the check runs on the emitted Go's own edges or table literals. A candidate with more than one possible event history, or one whose check ran out of budget, is refused, and the NFA is used. Test: `TestConstructionRejectsCorruption`.
3. **Typed results.** The emitted matcher writes each capture that takes part at most once in its scope to fixed local positions and builds `T` from them: it allocates no event storage, and a parse that returns only such captures allocates nothing beyond what `T` holds. A repeated capture allocates its result slice; any temporary storage is counted and shown by `ergo inspect`. Test: `TestPatternLoweringAgrees` (counted allocations on generated patterns with no repeated capture are zero beyond the result).
4. **Linear find.** For a fixed pattern, `FindT` reads each byte of `s` a bounded number of times: it runs the starts of all positions in one pass, in priority order, and does not restart at each position after a failed prefix. Test: `TestPatternLoweringAgrees` (a work counter that the matcher exposes grows linearly with the input on failing inputs, at small sizes; no time is measured).
5. **Shared and cached.** Patterns with equal canonical plans, grammar profile, mode and capture names share one matcher; each derived type keeps its own typed adapter. A cache of built matchers is keyed by that identity and the compiler's build identity, so a matcher built by another compiler build is never reused. A pattern that no reachable code uses emits nothing. Test: `TestBudgetsAndIdentity`; `TestPatternLoweringAgrees`.
6. **The reference lowering.** `-X lower.patternLowering=reference` builds every pattern as the canonical NFA. It stays selectable, every example's `ergo test` passes under both lowerings, and the derived laws compare each parse with it in the test build. Test: `TestPatternLoweringAgrees`: on seeded generated patterns and inputs, each branch of the lowering reached, the selected engine and the reference give equal results; a failing seed prints the Ergo source, both outputs and the replay command.
7. **Speed.** The target for a parse is at most the instructions of the faster of Go's `regexp`, compiled once beforehand and producing the same captures, and an optimised hand-written scanner, with no more bytes and allocations per operation than that scanner. A claim of speed is measured on the same host at base and head; until then none is made. Test: none; receipts are measured.

## Coherence with refinements and codecs

1. **Refine is the refinement.** `type N derive Refine(Of[B](), lo, hi)` derives exactly the bundle of `type N B where lo <= it && it <= hi` (or `len(it)` for a `string` or slice base): `ParseN` with the failure `Unsatisfied`, the law `NWhere`, the ways in, the ways out, the zero-value rule and the generator of [Refinements](types.md#refinements). A refinement with `where` and its plan form share one implementation of these. Test: `TestDeriveModel`: for generated spans, the two declarations give equal derived declarations, diagnostics and generated values.
2. **Codecs are unchanged.** A derived type has the wire form the [codec rule](types.md#codecs) gives the same declaration written by hand: a pattern record of strings is wire by its structure, and a derive adds no `Wire`. A `Wire(x T) string` written in the file of the derive forms a by-name pair with the derived `ParseT`, and the pair's round-trip law is derived as for a written pair. The derivation never invents an inverse of a parse. Test: `TestDeriveModel`.
3. **Authority is unchanged.** A derived record is an ordinary record: construction authority follows its field names as for a written record ([types](types.md#structs)), and the derive gives no guarantee that a value spells a word of its pattern. A type that guarantees its original text keeps that text in an unexported field and is built only by its own package's parse function. Test: `TestDeriveModel`.
4. **Rows are unchanged.** `ParseT` fails `NoMatch` only; a caller that converts a capture further (a number's range, a calendar date) writes that conversion and its failure row. Test: `TestDeriveModel`.

## Expansion examples

```ergo
type Date derive Pattern(`(?<Year>[0-9]{4})-(?<Month>[0-9]{2})(-(?<Day>[0-9]{2}))?`)
// type Date { Year string, Month string, Day Option[string] }
// func ParseDate(s string) Date fails NoMatch
// func FindDate(s string, from int) Option[(Date, int, int)]
// law DateMatches(s string) …; law DateAccepts(w string) …
// ParseDate("2026-11") is Date{Year: "2026", Month: "11", Day: None{}}
// ParseDate("2026-1x") fails NoMatch{Type: "Date", At: 6}

type Query derive Pattern(`(?<Pair>(?<Key>[a-z]+)=(?<Value>[0-9]*)&?)*`)
// type QueryPair { Key string, Value string }
// type Query { Pair []QueryPair }
// ParseQuery("a=1&b=") is Query{Pair: []QueryPair{{Key: "a", Value: "1"}, {Key: "b", Value: ""}}}

type Path derive Pattern(`(/(?<Seg>[a-z0-9]+))+`)
// type Path { Seg []string }

type Port derive Refine(Of[int](), 1, 65535)
// the bundle of: type Port int where 1 <= it && it <= 65535

// package ids, a user package
func Fixed(prefix string, width int) TypePlan {
	Pattern("\(prefix)-(?<Num>[0-9]{\(width)})")
}

func Paged(item TypePlan) TypePlan {
	Record([]FieldPlan{Field("Items", List(item)), Field("Next", Optional(Of[string]()))})
}

// package app
import app/ids

type Ticket derive ids.Fixed("TK", 6)
// type Ticket { Num string }; ParseTicket("TK-004211") is Ticket{Num: "004211"}
// ids.Fixed("T(K", 6) is S108 at the ( in app's literal "T(K": "unclosed group", derived at the Ticket line

type Users derive ids.Paged(Of[User]())
// type Users { Items []User, Next Option[string] }: wire by its structure; no ParseUsers, since a record plan parses no text
```

## Prior art

- CTRE (C++) parses a pattern in templates at compile time and specialises the matcher; it admits backreferences, so it is not a DFA guarantee. Zig's `ctregex` uses `comptime` with an evaluation branch quota, the model of the plan budget. ArkType's `arkregex` infers capture types in TypeScript's type checker and documents its depth limits; Ergo derives types from a value at one declaration instead of a type-level parser.
- Tyre and `ppx_tyre` (OCaml) type captures as options, lists, records and variants, the model of the capture shapes; their matcher is the `Re` library at run time, and a match on them has no exhaustiveness check. Ergo's union form is matched exhaustively.
- re2c emits direct Go control flow from regular expressions with tags for submatches, last-position and history tags, and a choice between code and tables; Ragel emits tables or code with actions. Ergo takes ordered history tags and the code/table choice, with the construction check.
- Rust removed its compile-time `regex!` plugin in 2017 as unmaintained and slower than the run-time engine; its 1.13 `regex!` compiles lazily at run time. `regex-automata` keeps several engines (PikeVM, bounded backtracker, one-pass, DFAs) apart and selects by measurement. Ergo keeps the NFA reference selectable and makes no speed claim before receipts.
- Go's `regexp` compiles at run time and selects one-pass, bounded backtracking or an NFA; it returns the last occurrence of a repeated group. Ergo returns every occurrence.
- Elixir's `~r` sigil compiles a literal pattern during compilation and embeds the result for Erlang's `:re` (PCRE); captures are maps. ReScript's `%re` and its regex literals emit a JavaScript regex and leave matching to the host engine. Gleam's `gleam/regexp` binds the target's engine and returns positional `Option` captures. None derives a typed record or union from a pattern, and each matches in the host's run-time engine; Ergo checks the pattern at check time and emits a matcher per pattern.
- MoonBit documents checked regex literals and their composition, with first and longest match forms.
- Effect Schema derives a decoder, an arbitrary and typed issues from one schema value at run time; Ergo takes that purpose (one declaration gives a parser, its failure, a generator and laws) as check-time declarations, without a schema object at each match.

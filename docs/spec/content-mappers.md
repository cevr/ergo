# Content mappers

Status: **designed**: no compiler support yet for file-level asset imports or the userland content-mapper contract; the parser and checker handle only inline `type T derive E` type-plan and `Provider` derives.

A **content mapper** is an ordinary, pure, capability-free Ergo function evaluated at check time that maps external or inline domain bytes (`ContentInput`) into in-memory Ergo declarations (`MappedErgo`), a deterministic byte-span source map (`SourceSpanMap`), and structured asset diagnostics (`MapperDiag`). One contract serves both **file-level asset imports** (such as `.sql`, `.re`, schema, or route files) and **inline derived types** (`type T derive <mapper>(...)`). Mapped declarations live strictly in memory and in `lawcache`, never on disk as checked-in generated files, pass through the normal compiler frontend (`syntax`, `check`, `facts`, `lower`), map diagnostics and origins back to the originating asset or literal byte, and run with zero privilege difference between standard-library packages and userland packages.

## Core contract

Every content mapper implements one pure contract over closed inputs and total outputs.

```ergo
type AssetOrigin =
	| FileAsset{Path string, Package string}
	| InlineDerive{Package string, File string, Target string, Line uint32, Col uint32}

type CompanionAsset {
	Path  string,
	Bytes []byte,
}

type ContentInput {
	Target     string,
	Origin     AssetOrigin,
	MediaKind  string,
	Bytes      []byte,
	Companions []CompanionAsset,
}

type SourceSpan {
	Start uint32,
	End   uint32,
}

type SpanMapping {
	Generated SourceSpan,
	Companion Option[uint32],
	Source    SourceSpan,
	Label     string,
}

type SourceSpanMap {
	Entries []SpanMapping,
}

type DiagSeverity = Error{} | Warning{}

type MapperDiag {
	Severity  DiagSeverity,
	Companion Option[uint32],
	Span      SourceSpan,
	Code      string,
	Message   string,
	Hint      string,
}

type MappedErgo {
	Source   string,
	Spans    SourceSpanMap,
	Diags    []MapperDiag,
	Digest   []byte,
}

type Mapper = func(ContentInput) MappedErgo
```

1. **Mapper signature.** A content mapper is an ordinary exported or unexported function of type `func(ContentInput) MappedErgo` (or a registered `Mapper` value bundling that function with its media metadata). No macro syntax, compiler plugin binary, or special declaration keyword is required to define one.
2. **Closed companions over ambient discovery.** A mapper receives only `ContentInput`. Any companion files required to check the primary payload (such as DDL schema files for SQL query checking) arrive explicitly in `ContentInput.Companions`, ordered canonically by path. Mappers never scan directories or read ambient host state.
3. **Total span bounds and monotonicity.** Every entry in `SourceSpanMap.Entries` satisfies `0 <= Generated.Start <= Generated.End <= len(Source)` and `0 <= Source.Start <= Source.End <= len(OriginBytes)` (where `OriginBytes` is `ContentInput.Bytes` when `Companion` is `None`, or `Companions[i].Bytes` when `Companion` is `Some{Value: i}`). Entries are sorted by `Generated.Start` and mutually disjoint (`prev.Generated.End <= next.Generated.Start`). Invalid spans or out-of-range companion indices are refused at check time as a diagnostic on the mapper binding or derive site.
4. **Error termination before frontend parsing.** When `MappedErgo.Diags` contains any `Error` diagnostic, the compiler reports those diagnostics at their mapped `SourceSpan` and skips parsing `MappedErgo.Source`. Reporting stops cleanly at the asset mistake instead of cascading through broken generated syntax (`One mistake, one diagnostic`).
5. **In-memory synthesis only.** Mapped `.ergo` declarations exist only inside compiler memory and `lawcache`. Builds never emit generated `.ergo` or `.go` source files into package source directories, eliminating codegen staleness and commit noise.
6. **Inspection through `ergo inspect`.** `ergo inspect` (human text and JSON views) prints the synthesized declarations, type signatures, `fails`/`needs`/`given`/`gives`/`keeps`/`where` contracts, derived laws, active `lawcache` key, and `SourceSpanMap` provenance for each imported asset or inline `derive`.

## Registration and invocation

Both file-level asset imports and inline `type T derive <mapper>(...)` declarations lower into the same check-time evaluator over `ContentInput`.

```ergo
import "./queries/users.sql"

func LoadEmail(id int64) Option[string] fails users.QueryErr needs {users.DB} {
	match try users.GetUser(id) {
	case Some{Value: u}:
		return u.Email
	case None:
		return None
	}
}
```

And inline derived type mapping:

```ergo
import "std/regex"
import "acme/ids"
import "acme/sql"

type Date derive regex.Pattern("^(?P<year>[0-9]{4})-(?P<month>[0-9]{2})-(?P<day>[0-9]{2})$")
type Ticket derive ids.Fixed("TK", 6)
type ActiveUser derive sql.Query("SELECT id, email FROM users WHERE id = $1 AND bio IS NOT NULL")
```

1. **File-level asset imports.** A module manifest (`ergo.mod`) associates a file extension or pattern with a pure mapper function and zero or more ordered companion patterns (for example binding `*.sql` to `sql.MapFile` with `schema/*.sql` companions). An Ergo source file imports the asset by relative or module path; the checker validates that the path resolves within the module, builds `ContentInput` with `Origin = FileAsset{...}`, evaluates the mapper, and checks the resulting package declarations as an ordinary imported Ergo package named after the file stem unless aliased for collision.
2. **Inline derived mapping.** In `type T derive E`, the checker evaluates `E` as a closed check-time constant expression. When `E` returns a content-mapper plan pairing a `Mapper` function with its serialized payload (`Target: "T"`, `Origin: InlineDerive{...}`, `MediaKind`, `Bytes`, plus any declared module companions), the checker executes the identical pure mapper evaluator and binds the resulting declarations under `T`'s package unit.
3. **One pipeline for both forms.** File-level imports (`FileAsset`) and inline derives (`InlineDerive`) share one evaluator, one budget checker, one `lawcache` store, one frontend compiler pass (`syntax`, `check`, `facts`, `lower`), and one `SourceSpanMap` origin resolver. No special domain logic exists for either form.
4. **Package namespace and name hygiene.** Declarations produced by a file-level import belong to that asset's synthetic module namespace and obey standard capitalization visibility ([grammar.md](grammar.md#visibility)). Declarations produced by `type T derive E` live in the package declaring `T`, must declare `T` (plus `T`-prefixed helpers such as `ParseT`, `FindT`, `TMatches`, `TAccepts`), and participate in ordinary package collision checks (duplicate exported or local names fail check cleanly).
5. **Reproducible origin offsets.** For inline string literals inside `type T derive E`, `SourceSpan` covers the exact UTF-8 byte range inside the literal after escape decoding, enabling diagnostic columns to land directly on the offending character of the pattern or inline query while pointing related information to the enclosing `derive` declaration.

## Pure evaluation, budgets, and hermeticity

Compile-time content mapping executes inside a strict capability-free subset so builds remain hermetic, parallelizable, and independent of machine state.

1. **Zero capabilities at check time.** A content mapper function and every function in its transitive call graph must have an empty `needs` row (`pure`). Accessing `fs`, network sockets, wall clock, randomness, environment variables, OS processes, or actor handles fails check with a diagnostic naming the mapper declaration and the disallowed callee.
2. **No foreign Go escape hatches.** A content mapper cannot call `import go` or `extern` FFI functions at check time. All check-time logic runs in checked pure Ergo evaluated by the compiler's deterministic check-time engine.
3. **Deterministic semantics across hosts.** Map iteration inside check-time evaluation visits keys in canonical sorted order, integer arithmetic uses fixed-width two's-complement rules, string indexing and slicing operate on UTF-8 bytes deterministically, and no runtime pointer addresses or nondeterministic iteration orders may escape into `MappedErgo.Source` or `Digest`. Two runs on different architectures produce byte-for-byte identical outputs.
4. **Finite resource budgets.** Every mapper invocation enforces deterministic compile-time budgets, independent of wall-clock duration:
   - **Step budget**: maximum check-time evaluator operations (default `1_000_000` steps).
   - **Recursion depth**: maximum call stack depth (default `128` frames).
   - **Input and output byte budgets**: bounded `ContentInput` total byte size and bounded `MappedErgo.Source` byte length (default `2 MiB` each).
   - **Artifact budget**: bounded count of entries in `SourceSpanMap.Entries` and `Diags`.
   Exhausting any budget stops evaluation cleanly, reports a diagnostic at the import or derive site naming the exhausted limit and active call frame, and never triggers fallback host execution.
5. **Stratified dependency order and cycle detection.** Before evaluating any content mapper, the checker resolves package and asset import edges. A package containing a mapper `M` cannot transitively import an asset or derived type evaluated by `M`, nor can an asset or inline derive reference a declaration whose type depends recursively back on itself. Any dependency cycle fails cleanly with a cycle diagnostic listing every edge in the cycle (`A -> B -> A`).

## Caching and incremental invalidation

Mapped declarations are cached cleanly in `lawcache` without ever polluting working trees with generated code.

1. **Content-addressed cache key.** Every valid mapper evaluation computes a cryptographic key `MapperCacheKey` stored in `lawcache` (`laws/v1/`):
   - `CompilerBuild`: compiler frontend contract and check-time evaluator build identity.
   - `MapperSemanticDigest`: structural hash of the mapper function's canonical checked IR and all transitive pure callees, excluding comments, formatting whitespace, and source positions.
   - `Target` and `MediaKind`: declared target identifier and asset format tag.
   - `BytesDigest`: cryptographic hash of `ContentInput.Bytes`.
   - `CompanionDigests`: cryptographic hash of ordered `(Path, BytesDigest)` pairs for every companion in `ContentInput.Companions`.
2. **Rebasing origin metadata.** `SourceSpanMap` stores byte offsets relative to `ContentInput.Bytes` and `Companion` indices, while `AssetOrigin` (file path or inline line/column) is attached when binding diagnostics or inspect records to a call site. Editing unrelated lines or comments above an inline `derive` therefore leaves `(Target, MediaKind, BytesDigest, CompanionDigests, MapperSemanticDigest)` unchanged and reuses the cached `MappedErgo.Source` and downstream compiled IR without re-running the mapper, while diagnostics still report the current line and column.
3. **Precise invalidation.** Changing any byte of the asset payload, adding or modifying a bound companion schema file, altering the semantic body of the mapper or a pure helper it calls, or upgrading the compiler build changes the cache key and forces exact recomputation. Changing only comments or non-semantic formatting inside the mapper's `.ergo` source preserves `MapperSemanticDigest` and keeps the cache hit.
4. **Never cache non-deterministic or aborted runs.** Budget exhaustion, dependency cycle errors, invalid `SourceSpanMap` bounds errors, or internal evaluator rejections are NEVER written to `lawcache`. Only complete, bounds-verified `MappedErgo` outputs (including deterministic asset diagnostics emitted by the mapper itself) enter `lawcache`.
5. **Export cutoff propagation.** Once `MappedErgo.Source` passes frontend typechecking, its package exports participate in ordinary incremental compilation: editing a SQL query body whose inferred input parameters and output row record stay identical stops invalidation at export cutoff rather than cascading across callers.

## Source-mapped compiler pipeline and diagnostics

Every mapped output enters the standard compiler frontend cleanly so checks, facts, resource ownership, and diagnostics compose without seams.

1. **Ordinary frontend compilation.** When `MappedErgo.Diags` has no errors, `MappedErgo.Source` flows verbatim through the standard pipeline:
   - `syntax.Parse` parses ordinary Ergo source tokens (with zero privileged parser bypasses).
   - `check` typechecks records, sum unions, `fails` rows, `needs` capability rows, borrows, exhaustive `match`, and `derive Provider(...)` test doubles.
   - `facts` verifies `given`, `gives`, `keeps`, and record `where` invariants ([facts.md](facts.md#facts), [facts.md](facts.md#record-invariants)).
   - `lower` compiles checked declarations, codecs, and co-located `law`/`test` blocks into native Go (`ERGO_FALLBACK=off` always holds; text fallback is never invoked).
2. **One mistake, one diagnostic.** Diagnostic reporting distinguishes asset mistakes from mapper bugs and caller mistakes so every broken input yields one precise diagnostic:
   - **Asset error (`MapperDiag`)**: Reported directly at the mapped byte range inside the external asset file (for instance `queries/users.sql:14:9`) or the exact UTF-8 column of an inline literal, with a single related-location frame pointing to the `import` or `type T derive ...` site. Generated source is not parsed, preventing duplicate cascade errors.
   - **Generated-code error (mapper defect)**: When valid asset syntax causes a mapper to produce ill-typed or invalid Ergo code (such as referencing an undeclared type or failing exhaustiveness), the checker resolves the failing AST span through `SourceSpanMap` back to the responsible asset span, cites the mapper declaration as the primary blame for an internal mapping bug or contract violation, and shows the synthetic snippet in `ergo inspect`.
   - **Caller misuse**: When caller Ergo code supplies a wrong type, forgets a `needs` capability, or leaves a `fails` case unhandled on a mapped function, the diagnostic points at the caller expression with one related location pointing through `SourceSpanMap` to the SQL parameter, nullable column, or pattern capture that defined that signature.
3. **End-to-end provenance.** Lowered panic/defect traces, law counterexamples, and `ergo inspect` origin frames retain the resolved asset origin (`FileAsset` path + byte line/col or `InlineDerive` site), keeping runtime error messages grounded in the author's SQL query or pattern literal rather than synthetic identifiers.

## Zero std privilege and separation from Provider derives

All content mappers, whether built into standard packages or written in user modules, share one public contract and compiler path.

1. **Zero privilege for `std/regex`.** Standard library regex patterns (`type Date derive regex.Pattern(...)`) use the exact same public `ContentInput -> MappedErgo` contract and budget-checked evaluator as userland mappers (`sql`, `ids`, `graphql`, `csv`, `route`). No compiler pass inspects package path `"std/regex"` or symbol `"Pattern"`.
2. **Retirement of privileged pattern plans.** Compiler-special-cased pattern AST plans (`internal/sketch/regexpplan`), `derive.Native` IR bypasses, and unused macro-like constructors on `TypePlan` (`Record`, `Union`, `Optional`, `List`, `Refine`) retire cleanly into ordinary content mappers that generate real Ergo `type`, `where`, `func`, and `law` declarations. Scalar and record predicates use first-class `where` invariants ([facts.md](facts.md#record-invariants)) rather than separate macro plan trees.
3. **Equal optimization and diagnostic rights.** Because userland mappers emit ordinary Ergo functions (including deterministic DFA/table byte matchers for patterns or typed codecs for IDs) compiled via standard `lower`, a userland `regex`, `route`, or `sql` package produces identical native Go quality, identical law execution in `ergo test`, and identical source-mapped diagnostics to `std/regex`.
4. **Separation from `Provider(...)` capability derives.** Capability-member derives (`type T[S] derive Provider(Of[S](), Of[F](), Of[N]())`, used for test doubles such as `testing.Script[S]`, `testing.Faulty[S]`, and `testing.FuncProvider[S]`) remain a distinct, non-content-mapper mechanism ([patterns.md](patterns.md#provider-plans)). `Provider(...)` derives inspect the declared operations of an Ergo capability type `S` inside the compiler's own type checker — just as derived codecs inspect record fields — rather than parsing external byte assets into generated Ergo text. Keeping capability reflection cleanly separate prevents test doubles from serializing and re-parsing Go/Ergo signatures through source strings while keeping external domain mapping purely byte-in, Ergo-out.

## Concrete examples

### Example 1: SQL-first typed query compilation (Reylo style)

Given an explicit schema companion (`db/schema.sql`) and query asset (`queries/users.sql`), a pure userland `sql.MapFile` content mapper generates checked Ergo record types, explicit `Option[T]` nullability, `where` invariants from column `CHECK` constraints, `fails QueryErr`, and `needs {DB}` capability bindings without touching any database server or filesystem at compile time.

Companion schema (`db/schema.sql`):

```sql
CREATE TABLE users (
    id INTEGER PRIMARY KEY NOT NULL,
    email TEXT NOT NULL,
    bio TEXT,
    role TEXT NOT NULL CHECK (role IN ('member', 'admin'))
);
```

Primary asset (`queries/users.sql`):

```sql
-- name: GetUser :one
SELECT id, email, bio, role
FROM users
WHERE id = $1;

-- name: CreateUser :exec
INSERT INTO users (email, bio, role)
VALUES ($1, $2, $3);
```

In-memory `MappedErgo.Source` synthesized by `sql.MapFile` (cached in `lawcache`, inspectable via `ergo inspect`, never written to disk):

```ergo
type Role = Member{} | Admin{}

type QueryErr = NotFound{} | ConstraintViolation{Detail string} | DecodeErr{Column string}

type UserRow {
	Id    int64,
	Email string,
	Bio   Option[string],
	Role  Role,
}

type DB capability {
	QueryOne(sql string, id int64) Option[UserRow] fails QueryErr
	Exec(sql string, email string, bio Option[string], role Role) () fails QueryErr
}

func GetUser(id int64) Option[UserRow] fails QueryErr needs {DB} {
	return DB.QueryOne("SELECT id, email, bio, role\nFROM users\nWHERE id = $1;", id)
}

func CreateUser(email string, bio Option[string], role Role) () fails QueryErr needs {DB} {
	return DB.Exec("INSERT INTO users (email, bio, role)\nVALUES ($1, $2, $3);", email, bio, role)
}
```

And an inline typed query with a checked non-null predicate (`WHERE bio IS NOT NULL`) refines the generated row directly via `type ... where`:

```ergo
import "acme/sql"

type ActiveUser derive sql.Query("SELECT id, email, bio FROM users WHERE id = $1 AND bio IS NOT NULL")

func GreetActive(id int64) string fails sql.QueryErr needs {sql.DB} {
	let row = try QueryActiveUser(id)
	return "hello " + row.Email + ": " + row.Bio
}
```

If an author edits `queries/users.sql` to select `SELECT id, bogus_col FROM users WHERE id = $1;`, `sql.MapFile` emits one `MapperDiag` covering `[Start, End)` of `bogus_col` in `queries/users.sql`, reporting at `queries/users.sql:2:12` ("column bogus_col does not exist on table users; companion db/schema.sql:1:1") with one related location at the `import "./queries/users.sql"` line, skipping `MappedErgo.Source` parsing cleanly.

### Example 2: Zero-privilege `regex.Pattern` and userland `ids.Fixed`

Both standard-library regex patterns and user-authored domain IDs use the exact same `type T derive <mapper>(...)` pathway:

```ergo
import "std/regex"
import "acme/ids"

type Date derive regex.Pattern("^(?P<year>[0-9]{4})-(?P<month>[0-9]{2})-(?P<day>[0-9]{2})$")
type Ticket derive ids.Fixed("TK", 6)

func ParseBoth(dateText string, ticketText string) (Date, Ticket) fails DateParseErr, ids.InvalidId {
	let d = try ParseDate(dateText)
	let t = try ParseTicket(ticketText)
	return (d, t)
}
```

Here `regex.Pattern` evaluates at check time via the exact same pure evaluator contract as `ids.Fixed`:
- `regex.Pattern` builds a deterministic byte-matching table and capture extraction functions in ordinary Ergo (`Date` record with `Year string, Month string, Day string`, `ParseDate`, `FindDate`, `DateMatches`, `DateAccepts` laws), mapping invalid escapes or duplicate capture names directly to the byte offset inside the string literal.
- `ids.Fixed("TK", 6)` builds `type Ticket { raw string }` (unexported field preserving construction authority), `where ValidTicket(it)` (prefix `"TK"` and length `8` with decimal digits), `ParseTicket(s string) Ticket fails InvalidId`, `FormatTicket(t Ticket) string`, and co-located round-trip `law TicketRoundTrip(n uint32) { ... }` — with zero knowledge of `ids` inside the compiler binary.

## Required laws and verification properties

Every conforming compiler host, `lawcache` implementation, and userland content mapper must preserve five machine-checked invariants:

1. **Determinism.** Calling a content mapper twice with equal `(CompilerBuild, MapperSemanticDigest, Target, MediaKind, Bytes, CompanionDigests)` always yields byte-identical `MappedErgo.Source`, equal `SourceSpanMap`, and equal `MapperDiag` entries regardless of host OS, architecture, or map iteration layout.
2. **Cache soundness and exact invalidation.** `lawcache` hits if and only if all key parts agree. Mutating asset bytes, companion schema bytes, mapper semantic body (or transitive pure callees), or compiler build invalidates immediately, while unrelated edits outside the mapper semantics or literal payload preserve cache reuse. Budget-exhausted, invalid-span, or cyclic evaluations never write valid cache records.
3. **Span bounds and origin round-trip.** Every mapping in `SourceSpanMap` lies strictly within `[0, len(Source))` and `[0, len(OriginBytes))`. Every generated declaration diagnostic resolves monotonically back to its exact asset or companion byte span and attaches the import/derive declaration as its related site.
4. **Cycle termination.** Dependency cycles across file imports, inline `derive` declarations, and companion assets are always detected and terminated with a cycle diagnostic before emission or caching.
5. **Zero-privilege equivalence.** Standard-library mappers (`std/regex`) execute under the identical capability checks, budgets, cache keys, frontend compilation stages, and diagnostic rules as userland mappers (`acme/sql`, `acme/ids`).

## Prior art

- **TypeScript 7.1 content mappers** (`ergo-ts`, [tools/ts/README.md](../../tools/ts/README.md)): non-code imports (`.css`, `.sql`, schema assets) are mapped in-memory to synthetic typed declarations with source maps so language tooling navigates directly back to source assets without checked-in generated files. Ergo adopts zero-disk synthesis and byte source maps while forbidding arbitrary host Node/compiler plugins in favor of pure, budgeted Ergo functions.
- **Reylo (`reylosql/reylo`) and `sqlc`**: SQL-first typed query compilation parses `.sql` queries against DDL schemas to generate typed inputs, row types, and nullability wrappers. Ergo maps nullable columns directly to `Option[T]`, database errors to `fails` rows, execution context to `needs`, and SQL predicates to `where` invariants.
- **Rust procedural macros and `sqlx`**: Rust proc-macros execute arbitrary host native code (capable of filesystem, network, and clock access) producing opaque token streams without cross-file asset span contracts, and `sqlx` queries live databases during compilation. Ergo replaces host-native proc-macros and live build-time database probes with capability-free pure mappers over explicit `Companions`.
- **Elixir sigils/macros, MoonBit, and Effect Schema**: Sigils and macros provide compact custom literal DSLs, while Effect Schema derives parsers, generators, and typed issues from schema descriptions. Ergo unifies asset imports and inline literals into one source-mapped, law-generating mechanism compiled down to native code with `ERGO_FALLBACK=off`.

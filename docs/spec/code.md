# Code: questions about a workspace's code

Status: **partly implemented**: the fact tables (SEMTOOLS SEM0), `Reader` (`Describe`, `Symbols`, `Symbol`, `References`), its providers `Index` and `Scan`, and `ergo code` (SEM1); `Planner`, `Writer` and the `Checker` and `Files` seams with their memory providers `Model` and `Disk` (SEM4a); `Rename` with the proof `Alpha` and the refusals `NameTaken`, `OutsideWorkspace` and `Capture`, over `Model` (SEM5's core); `Calls` and `Api`, a plan's contract impact, `ergo code calls|api`, and `ergo inspect api` as a view of `Api` (SEM2a); `Reach` and `MachineOf`, `ergo code reach|machine-of`, and `ergo inspect machine` as a view of `MachineOf` (SEM2b); `Reach` of a failure and of a provider, and `ergo inspect machine --json` (SEM2c, SEM2d, SEM2e). The server on `ergo dev`, MCP and HTTP (SEM3), the product `Checker` and `Files`, `ergo code plan|apply` and `ergo fix` on plans (SEM4b, after README Gap 17; with them rename covers parameters, locals, variants, laws and tests), patterns (SEM6) and rewrites (SEM7) are designed (`semtools/plan.md` of the wf10 run).

Std package `code` answers questions about code from the **fact tables** the checker's result gives for each package ([GLOSSARY](../../GLOSSARY.md), Fact table). No tool walks typed trees to answer a name question, and none reads source text with a regular expression.

## The fact tables

1. **Built from the check.** `internal/sketch/facts` builds the tables of one checked package from `check.Info` in one walk of its files: a symbol per declaration and per name it resolves, a site per name with its kind, the enclosing declaration of each site, and the relations (union to variant, caller to callee, operation to the function that provides it at a conversion, function to each capability and each failure the checker derived for it, call to each capability it brings its caller or that a with around it discharges, provider to each capability a with binds to it at a call, call to each failure it raises in its caller or that a catch around it handles, machine graphs). Equal programs give equal bytes (`facts.Encode`, version 3). `TestTablesAgreeWithScan` holds the tables to a scan of every map of `check.Info`, on generated programs and on the examples.
2. **Handles.** A declaration is named by its handle: `path.Name`; `path.Type.Member` for a field, a variant's field, an operation or a member law; `path.Func.param` for a parameter or a type parameter; `path.Func.local` for a local its declaration binds once. A function of an overloaded name (types.md, Methods) takes its first parameter's type before its name: `path.Type.Name`. A law's handle ends `#law` and a test's `#test`. The root package's handle uses its path in the module, though the checker names it `main`. A local its declaration binds more than once has no handle; its address is its position, and the tables print that position as its handle.
3. **Kinds.** A symbol's kind is one of `package`, `func`, `type`, `variant`, `field`, `operation`, `machine`, `law`, `test`, `context`, `let`, `param`, `typeparam`, `local`. A site's kind is one of `def`, `read`, `write`, `call`, `typeuse`, `construct`, `match`, `import`, `needs`, `fails`, `provides`, `binds`, `handles`.
4. **A package that does not check** gives the rows the checker resolved, and every answer over it says `complete: false`; `Describe` counts its errors. A package that imports a broken package is not checked and gives no rows. How many of a broken package's names the checker resolves is not measured; it depends on where the error is.

## Reader

```
type Reader {
	Describe() Summary,
	Symbols(q SymbolQuery) SymbolPage,
	Symbol(a string) Decl fails NoSymbol | Ambiguous,
	References(q RefQuery) SitePage fails NoSymbol | Ambiguous,
	Calls(q CallQuery) CallPage fails NoSymbol | Ambiguous | NotCallable,
	Api(q ApiQuery) ApiView fails NoPackage,
	Reach(q ReachQuery) ReachPage fails NoSymbol | Ambiguous,
	MachineOf(a string) MachineView fails NoSymbol | Ambiguous | NotMachine,
	law HandleRoundTrips(pick int) { … },
	law ReferencesAreSound(pick int, kinds []string) { … },
	law ReferencesAreComplete() { … },
	law PagesPartition(pick int, size int) { … },
	law CallsAreSteps(pick int, depth int, inbound bool) { … },
	law ReachChainsAreCalls(pick int) { … },
	law MachineArmsStopAtAWholeArm(pick int) { … },
}
```

1. **Addresses.** `Symbol` and `References` take an address: a handle; a position, `file:line:column` with the file as a diagnostic names it, which names the symbol of the first site there; or a short name, the handle without its package (`Parse`, `Token.Kind`). A short name that fits one declaration names it; one that fits several fails `Ambiguous{Address, Candidates}` with every handle in table order; an address that names nothing fails `NoSymbol{Address}`.
2. **Queries.** `Symbols` finds declarations by name (exactly, by `prefix` or by `part`), kind and package; every field of `SymbolQuery` is optional. `References` gives the sites of one declaration, of the kinds asked for (all when none).
3. **Calls and Api.** `Calls` follows the tables' call edges, one per call site, from the declaration a call is in to the one it calls: a function, an operation, or a parameter or local holding a function value. `Direction` is `out` (the default) or `in`; `Depth` is 1 by default and at most 16. The answer is the hops, each a call with the number of steps that reach it, by depth, then in the order the step before reached each declaration; each declaration is followed once, so a cycle ends. A type, a type parameter, a field, a variant or a package fails `NotCallable{Address, Kind}`. `Api` is the contracts of one package's exported functions (the root package by default; `NoPackage` for a path the tables do not have): each one's signature, row, and what it takes and copies, as `ergo inspect api` prints them. `ApiDiff(old, new)` is the changed contracts, each saying whether it breaks resource callers (it newly takes a parameter or `self`, or newly copies a type parameter); `ApiText` and `ApiDiffText` are their text.
4. **Reach and machines.** `Reach` takes the address of a capability (an operation type), a failure or a provider (a predeclared one such as `prelude.Scheduler`, which no package of the tables declares, by its handle), and answers its `Kind`. For a capability or a failure it answers, a page at a time, each function whose needs or fails row, as the checker derived it, holds it, with one shortest chain of calls that brings it: each call is in the declaration the call before calls, each brings the capability or raises the failure (the callee's row at the site holds it, and no `with` around the call discharges it, or no `catch` around it handles it: the catch is total, or an arm tests the failure's type or a union that holds it, or matches a record pattern of it whose every field is a name or `_`; a record pattern that tests a field takes only some values, so the call both raises the failure and lists it as caught, and a chain passes it), and the last calls a declaration that brings it no further (an operation, a function value, a function whose row is written, a function that fails itself), or, when no chain ends so, calls back into a declaration already reached (a recursive function that fails itself). It also lists every call where a `with` discharges the capability or a `catch` handles the failure. For a provider it answers each function with a call whose capability a `with` binds to the provider (the innermost `with` that discharges it), and those calls; no chain. A call in a function literal is charged to the declaration that holds the literal. A provider is read in the package that declares the context its entry is in, so a `with` of an imported context names the import's provider. `MachineOf` is a machine declaration's graph (states, signals, initial state, arms with their variants and decisions, timers, work, unsure states) and the pairs a run can step from the initial state, each with the arms that may step it; any other declaration fails `NotMachine{Address, Kind}`. (The operation is not named `Machine`: a provider's function of that name would redeclare the prelude type.) `MachineText` draws a view as `ergo inspect machine` prints it.
5. **Pages.** Every list is a page in table order: `Offset` (0 by default) and `Limit` (100 by default, at most 1000), with `Total` and `Next` (-1 after the last page). At one revision the pages of an answer hold each row once (law `PagesPartition`).
6. **Revision.** Every answer carries the revision of the tables it read: a digest of the program's file names and texts.
7. **Laws.** `HandleRoundTrips`: the declaration at a declaration's handle is that declaration. `ReferencesAreSound`: each site answered names the declaration asked for, with a kind asked for. `ReferencesAreComplete`: the sites of every declaration, counted, are every site of the tables. `PagesPartition`: as above. `CallsAreSteps`: one more depth begins with the answer before and adds only hops of the new depth; at depth 1 each hop is a call of (or, in, to) the declaration asked about. `ReachChainsAreCalls`: each chain starts in its function, links each call to the next, brings or raises what was asked at every call and ends where it goes no further or back into the chain; no discharge brings a capability. `MachineArmsStopAtAWholeArm`: a pair's arms take it, in match order, and each but the last is partial. The top-level law `IndexIsScan` holds the product provider to the reference one on generated tables, `Calls` (Index walks by steps, Scan by rounds over every edge), `Api`, `Reach` and `MachineOf` (Index walks the states by steps, Scan by rounds with its own rule for an edge's targets) included. `ApiDiffOfItselfIsEmpty` and `ApiDiffBreaksOnANewTake` hold `ApiDiff`. `cmd/ergo`'s `TestCodeAnswersAgreeWithTheTables` holds `ergo code calls` at depth 1 to the checker's call sites on the examples, with a call through a function value in every package that has one; `reach` to the checker's derived needs and fails rows, to the calls in each `with`'s block that it discharges with their providers, and to the calls in each `catch`'s expression that it handles, and a function whose own call brings it has a chain; and `machine-of` and `ergo inspect machine`'s text to machinegraph's graph. They run in `internal/sketch/lower`'s `TestStdCodeLawsRun` ([stdlib.md, Std laws](stdlib.md#std-laws)).
8. **Providers.** `Index` (`IndexOf(t)`) indexes the tables once, by handle, by position and by short name, so a question reads the rows of its answer. `Scan` (`ScanOf(t)`) reads every row for every question: slow and plainly right. `NewIndex(seed)` and `NewScan(seed)` build each over `Generated(seed)`, the tables a law run draws. A program that reads code needs only `Reader`: its row shows it cannot write.

## Plans and apply

A change is a plan first. `Planner.Plan` computes a change's edits, checks the result, and stores the plan by its id; it writes nothing. `Writer.Apply` writes a stored plan, and nothing else in package `code` writes a source file. (SEMTOOLS SEM4a; the CLI, MCP and the product providers are SEM4b.)

```
type Planner {
	Plan(c Change) Planned fails BadChange | NameTaken | OutsideWorkspace | Capture,
	PlanOf(id string) Planned fails NoPlan,
	law PlanIsDeterministic(pick int, word int) { … },
	law PlanOfIsThePlan(pick int, word int) { … },
}

type Writer {
	Apply(id string) Receipt fails NoPlan | NotReady | Stale | Busy | IOFailed,
	Recover() Recovered fails Busy | IOFailed,
	law UnknownPlanIsNoPlan(id string) { … },
}

type Change = Replace{Of string, Text string} | Insert{After string, Text string}
	| Delete{Of string} | Fixes{Rules []string} | Script{Steps []Change}
	| Rename{Of string, To string}
type Proof = Alpha{} | SameCode{} | Checked{}
type Verdict = Ready{Proof Proof} | Blocked{Diagnostics []Diagnostic}
```

1. **Changes.** `Replace` puts `Text`, formatted, in place of the whole declaration at `Of`. `Insert` adds `Text` after the declaration at `After`. `Delete` removes the declaration at `Of`, and is refused while a site outside it names it. `Fixes` applies the fixes of the diagnostics whose code is in `Rules` (all codes when `Rules` is empty), checks again, and repeats until no fix is left. A diagnostic's fix is a list of edits (ergo fix's clause fix inserts its clause, own marks, parentheses and imports). Equal edits are made once, and insertions at one place go in the order the fixes list them; other edits that overlap are refused. A round that gives back the sources of an earlier round, or a run past `MaxFixRounds` (1024) rounds, is `BadChange`: no fixed point. `ergo fix`'s clause pass is this change, `fixes []` (SEM4b). `Script` runs `Steps` in order, each on the result of the step before. An address is taken as `Reader.Symbol` takes one; an address that names nothing, or several declarations, is `BadChange`.
2. **Edits.** An edit replaces a byte range found from the declaration's extent in the check. Edits of one plan that overlap, or that insert at the same place, are refused (`BadChange` names both). They are never merged.
3. **Formatting.** Only the text a change writes is formatted, so a declaration the change does not touch keeps its bytes (law `UntouchedKeepsBytes`). A file that was canonical stays canonical (`CanonicalStaysCanonical`).
4. **The overlay check.** The changed sources are checked as an overlay through the `Checker` seam.
   - When the overlay has a diagnostic the base does not have (counted by code and message), the plan is `Blocked` by those diagnostics (`OverlayDiagnosticBlocks`), and `Apply` refuses it with `NotReady`.
   - Otherwise the plan is `Ready`. Its proof is `SameCode` when the base checks and the lowered program is unchanged (`ReformatIsSameCode`). It is `Checked` when the code changed, or when the base did not check: in that case no proof is claimed. `Alpha` is a rename's proof ([Rename](#rename)).
5. **The plan.** A plan holds:
   - its id: a digest of the change and of the revisions it read; the same change at the same revisions gives the same id (`PlanIsDeterministic`);
   - every file it read, with that file's revision;
   - each file it changes, with the revision before, the revision after and the new text;
   - a line diff per changed file;
   - its verdict;
   - its contract impact: each public contract of the workspace the change alters, adds or removes, as `ergo inspect api --diff` gives it, named by package and name (law `ImpactIsTheChangedContracts`, against the `sig` and `needs` lines of `Model`'s sources).

   A store keeps the latest 64 plans. A planner holds no `Files`, so it cannot write (`PlanWritesNothing`).
6. **Apply.** `Apply` follows the protocol of the model `models/apply`, which `ergo test models/apply` proves in its scope:
   - It takes the workspace's lock (`Busy` when another applier holds it) and recovers any journal a dead applier left.
   - It reads every file the plan read. Unless each is at its revision, it fails `Stale` and changes nothing (`StaleChangesNothing`). A plan applied once is `Stale` the second time (`SecondApplyIsStale`).
   - It writes the journal (`intent`, with each changed file's revisions) and one temporary file per changed file, in that file's folder.
   - For each file it **exchanges** the temporary file and the path in one call, then **verifies** that what came out is the revision the plan read. When it is not, an outside write landed after the read: it restores and fails `Stale`.
   - It writes the journal (`committed`), then removes the temporary files and the journal. After it, the files are the plan's (`AppliedIsPlanned`), and planning the change again finds nothing to do (`PlanOfAppliedIsEmpty`).
7. **Recovery.** `Recover`, and `Apply` before it writes, read only the journal and the disk:
   - With no journal there is nothing to do.
   - With `committed`, they remove what is left.
   - With `intent`, they look at each file's temporary file. If it holds the new text, they remove it. If it holds the old text, they exchange it back when the path holds the new text, and otherwise remove it. If it holds other text, they exchange it back, so the outside write is kept.

   A recovery that stops part way starts again from the top. The law `ApplyIsSafe` runs the model on the product code. An applier stops, or one call fails, at any call, and an outside write may land before any call. Afterwards no outside write is lost, the other files are all new or all old, the files only read are unchanged, and no temporary file, journal or lock is left.
8. **Seams.** `Checker` (`Check`, `Format`) and `Files` (`Read`, `Write`, `Exchange`, `Remove`, `Exists`, `Lock`, `Unlock`) are operation types with laws.
   - Their memory providers are `Model` and `Disk`. `Model` is a small language with a checker, a formatter and fixable diagnostics. `Disk` can stop, fail once, or let an outside write in, at any call.
   - The product `Files` is `HostFiles` (`HostFilesOf(host, root)`, SEM4b): a workspace folder through an `fs.Host`. `Exchange` is `fs.Exchange` (stdlib.md, fs 4c), one host call: Linux's `renameat2(RENAME_EXCHANGE)`, macOS's `renamex_np(RENAME_SWAP)` (SEM4c). Where no atomic exchange exists (another host, a file system without the flag), `Exchange` fails `IOFailed` and apply is refused, with no fall-back to check-then-rename (the model's first mutant). `Write` is `fs.WriteFile` and makes missing folders; the lock is `fs.LockFile` on `.ergo/apply.lock`. `Exchange` first gives the new file the permission bits of the file it replaces, so a written file keeps its mode (bits `fs.Mode` cannot write are `IOFailed`, and nothing is written). A path under `.ergo/` (the lock and the journal) is in a state folder: the workspace's `.ergo` for `HostFilesOf`, any folder for `HostFilesIn(host, root, state)`. Its laws run under the named context `OnTestHost`, in the case's folder. `ergo fix` writes through it too (SEM4c): its clause, mode and lint passes give the files' new texts, and `code.Rewritten` makes them one `Ready{Checked}` plan, read at the texts the fix read, that `Apply` writes, so a file another writer changed meanwhile is `Stale` and no file is written. Its state folder is in the user's cache folder (`ergo/fix/<hash of the folder's path>`), so a fix leaves the user's folder holding what it held, but for the fixed files, each with its mode.
   - The product `Checker` wraps the Go checker and formatter (SEM4b): a provider that captures `go.Foreign` (README Gap 17), in `ergo code`'s linked tool.
9. **Out of scope** (as in the model): two outside writes to one file inside one apply; a writer that keeps the old file open and writes through it later; power loss.

*Prior art:* gopls's `go_rename_symbol` returns edits and writes nothing; go fix merges fixes against one baseline and drops the ones that conflict; LSP's `WorkspaceEdit` names the document versions it was computed from; Roslyn's FixAll may merge incorrectly. Here a plan names its revisions, refuses overlaps, and checks its overlay before anything is written, so a semantic conflict is a refused plan rather than a broken tree.

## Rename

`Rename{Of, To}` is a `Change`: the declaration at `Of`, and every site that names it, takes the name `To`. It is planned like any change, and its proof is `Alpha`. (SEMTOOLS SEM5's core, on the `Model` checker; the product checker's rows are SEM4b's.)

```
Plan(c Change) Planned fails BadChange | NameTaken | OutsideWorkspace | Capture

type NameTaken { Handle string, To string, By string }
type OutsideWorkspace { Handle string }
type Capture { Handle string, To string, Rows []string }
```

1. **The family.** An operation is renamed with the function of every type that provides its operation type, as the `Provides` edges of the check (`Report.Binds`) bind them. Renaming such a function renames its operation and the other providers' functions too.
2. **Refusals.**
   - `To` that is not a name is `BadChange`.
   - A family member outside the workspace (std or a dependency: `Report.Workspace` names the workspace's packages), or a site of one there, is `OutsideWorkspace`.
   - A name that a family member's scope declares already (its package, or its type for a field, an operation or a method) is `NameTaken`, with the declaration that has it.
3. **Edits.** Each site that the tables give is edited, and only the name's bytes are replaced: a qualifier or an import alias before the name keeps its bytes, and so does a comment that mentions the old name. A field's rename edits the keys of record literals too, since they are sites of the field.
4. **The proof.** The edited sources are checked again. Their tables must equal the tables before, under the name map: each old handle becomes its new one, and so does every handle under it (a type's members), and a column moves by the length change of each edit before it on its line. When a row differs, a use now resolves to another declaration (a local that shadows the new name), or a word now names the symbol. The plan is then refused with `Capture`, which lists the differing rows. Otherwise the plan is `Ready{Alpha}`, unless the result has a diagnostic the base did not have (counted by code), in which case it is `Blocked`.
5. **The law.** `RenameIsAlpha` compares each plan with a reference. `Program(seed)` draws a workspace as a tree: a dependency with an operation type, two packages with operation types and the types that provide them, fields read in bodies and set in record literals, an import alias, locals, words that name nothing, and comments that mention symbols. The reference renames inside that tree and renders it again. For every symbol and every drawn name, the plan's outcome must be the reference's: bad, outside, taken, capture, or `Ready{Alpha}` with the reference's bytes. The plan's five named mutants are each killed by this law: a field key left unrenamed, an operation renamed without its providers, a capture accepted, an import alias rewritten, a comment changed. So is a sixth: no `NameTaken`.
6. **The product's rows** (SEM4b). On the product checker (`ergo code plan`), parameters, locals, variants, laws and tests are renamed by the same rules: a parameter or a local is in its function's scope (`path.Func.param`); a law's and a test's handle keep their `#law` and `#test` ending; a parameter or a local stays unexported, and a test stays exported, whatever its new name. A function that provides an operation has the handle `path.Name` when its package declares the name once, and is in its operation's family all the same. `TestCodePlanOnTheProduct` renames each kind.
7. **Gaps.** `Model` has no rows for parameters, locals, variants, laws or tests, so `RenameIsAlpha` does not draw them; the locals of a function share its scope, so a name another local of the function has is `NameTaken` even in another arm. A rename to the old name and back gives the first bytes: that follows from `RenameIsAlpha`'s byte comparison, so it has no law of its own (AGENTS.md, Tests).

*Prior art:* Gleam's language server renames from the references its compiler recorded and refuses a name that is taken; gopls's rename checks for shadowing and conflicts before it returns edits (`renameCheck`); rust-analyzer refuses some renames that would change name resolution [recalled, not re-read]. Here, rather than a rule for each case, the check of the result is compared with the tables, so any capture is found by one comparison.

## ergo code

`ergo code [--in DIR] [--json] COMMAND [ARGS...]` checks the program in `DIR` (the current folder by default), builds its fact tables and answers one question through `Reader`'s command line, which `ergo gen cli` generates ([rpc.md](rpc.md), ergo gen cli): `describe`, `symbols [--name N] [--match M] [--kind K] [--package P] [--offset O] [--limit L]`, `symbol ADDRESS`, `references --of ADDRESS [--kinds K]... [--offset O] [--limit L]`, `calls --of ADDRESS [--direction in|out] [--depth N] [--offset O] [--limit L]`, `api [--package P]`, `reach --of ADDRESS [--offset O] [--limit L]`, `machine-of ADDRESS`. A record answer prints as its JSON. A typed failure prints its tag and its JSON on standard error with status 1, or its tagged JSON on standard output with `--json`:

```
$ ergo code symbol Total
Ambiguous: {"_tag":"Ambiguous","address":"Total","candidates":["shop/cart.Total","shop/stock.Total"]}
```

The command line is Ergo: std `code` and its generated `codecli`, in the module `internal/linked/code/ergo`, linked into the binary as checked-in Go (docs/tooling.md, Linked Ergo packages). The tables cross from the Go checker to the Ergo reader as the JSON of `code.Tables`, a seam that capstone S1's mixed binary removes. `ergo query`, `ergo type` and `ergo graph` are gone, with no alias. `ergo inspect api [--json] [--diff OLD NEW]` is a view of the same package: `ApiText`, `ApiDiff` and `ApiDiffText` over the tables of the root package (its contracts come from `internal/sketch/api`'s projection); `--json` keeps the shape of `api.Function` and `api.Change`, and the output on every example is byte-equal to the Go view it replaced. `ergo inspect machine FILE|DIR` is the same kind of view: `MachineOf` of each machine of the root package, drawn by `MachineText`, byte-equal on every example to the Go drawing it replaced; `--json` prints each machine's `MachineView` (the option parser refused `--json` for inspect machine before SEM2c, so no older JSON existed).

*Prior art:* Gleam's compiler records kinded references while it type-checks, and its language server's rename reads them; gopls keeps a per-package cross-reference index computed during type checking and serves `go_symbol_references` to agents by a symbol's name (`lib.T.M`); SCIP names symbols by printable strings; MoonBit's `moon ide` takes a symbol name on the command line. ReScript's editor tooling reads compiler artifacts from a second binary; here the tools are the compiler's own release.

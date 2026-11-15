# Code: questions about a workspace's code

Status: **partly implemented**: the fact tables (SEMTOOLS SEM0), `Reader` (`Describe`, `Symbols`, `Symbol`, `References`), its providers `Index` and `Scan`, and `ergo code` (SEM1); `Planner`, `Writer` and the `Checker` and `Files` seams with their memory providers `Model` and `Disk` (SEM4a). `Calls`, `Reach`, `Machine` and `Api` (SEM2), the server on `ergo dev`, MCP and HTTP (SEM3), the product `Checker` and `Files`, `ergo code plan|apply` and `ergo fix` on plans (SEM4b, after README Gap 17), rename (SEM5), patterns (SEM6) and rewrites (SEM7) are designed (`semtools/plan.md` of the wf10 run).

Std package `code` answers questions about code from the **fact tables** the checker's result gives for each package ([GLOSSARY](../../GLOSSARY.md), Fact table). No tool walks typed trees to answer a name question, and none reads source text with a regular expression.

## The fact tables

1. **Built from the check.** `internal/sketch/facts` builds the tables of one checked package from `check.Info` in one walk of its files: a symbol per declaration and per name it resolves, a site per name with its kind, the enclosing declaration of each site, and the relations (union to variant, caller to callee, operation to the function that provides it at a conversion, machine graphs). Equal programs give equal bytes (`facts.Encode`, version 1). `TestTablesAgreeWithScan` holds the tables to a scan of every map of `check.Info`, on generated programs and on the examples.
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
	law HandleRoundTrips(pick int) { … },
	law ReferencesAreSound(pick int, kinds []string) { … },
	law ReferencesAreComplete() { … },
	law PagesPartition(pick int, size int) { … },
}
```

1. **Addresses.** `Symbol` and `References` take an address: a handle; a position, `file:line:column` with the file as a diagnostic names it, which names the symbol of the first site there; or a short name, the handle without its package (`Parse`, `Token.Kind`). A short name that fits one declaration names it; one that fits several fails `Ambiguous{Address, Candidates}` with every handle in table order; an address that names nothing fails `NoSymbol{Address}`.
2. **Queries.** `Symbols` finds declarations by name (exactly, by `prefix` or by `part`), kind and package; every field of `SymbolQuery` is optional. `References` gives the sites of one declaration, of the kinds asked for (all when none).
3. **Pages.** Every list is a page in table order: `Offset` (0 by default) and `Limit` (100 by default, at most 1000), with `Total` and `Next` (-1 after the last page). At one revision the pages of an answer hold each row once (law `PagesPartition`).
4. **Revision.** Every answer carries the revision of the tables it read: a digest of the program's file names and texts.
5. **Laws.** `HandleRoundTrips`: the declaration at a declaration's handle is that declaration. `ReferencesAreSound`: each site answered names the declaration asked for, with a kind asked for. `ReferencesAreComplete`: the sites of every declaration, counted, are every site of the tables. `PagesPartition`: as above. The top-level law `IndexIsScan` holds the product provider to the reference one on generated tables. They run in `internal/sketch/lower`'s `TestStdCodeLawsRun` ([stdlib.md, Std laws](stdlib.md#std-laws)).
6. **Providers.** `Index` (`IndexOf(t)`) indexes the tables once, by handle, by position and by short name, so a question reads the rows of its answer. `Scan` (`ScanOf(t)`) reads every row for every question: slow and plainly right. `NewIndex(seed)` and `NewScan(seed)` build each over `Generated(seed)`, the tables a law run draws. A program that reads code needs only `Reader`: its row shows it cannot write.

## Plans and apply

A change is a plan first. `Planner.Plan` computes a change's edits, checks the result, and stores the plan by its id; it writes nothing. `Writer.Apply` writes a stored plan, and nothing else in package `code` writes a source file. (SEMTOOLS SEM4a; the CLI, MCP and the product providers are SEM4b.)

```
type Planner {
	Plan(c Change) Planned fails BadChange,
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
type Proof = Alpha{} | SameCode{} | Checked{}
type Verdict = Ready{Proof Proof} | Blocked{Diagnostics []Diagnostic}
```

1. **Changes.** `Replace` puts `Text`, formatted, in place of the whole declaration at `Of`. `Insert` adds `Text` after the declaration at `After`. `Delete` removes the declaration at `Of`, and is refused while a site outside it names it. `Fixes` applies the fixes of the diagnostics whose code is in `Rules` (all codes when `Rules` is empty), checks again, and repeats until no fix is left, at most 8 rounds. `Script` runs `Steps` in order, each on the result of the step before. An address is taken as `Reader.Symbol` takes one; an address that names nothing, or several declarations, is `BadChange`.
2. **Edits.** An edit replaces a byte range found from the declaration's extent in the check. Edits of one plan that overlap, or that insert at the same place, are refused (`BadChange` names both). They are never merged.
3. **Formatting.** Only the text a change writes is formatted, so a declaration the change does not touch keeps its bytes (law `UntouchedKeepsBytes`). A file that was canonical stays canonical (`CanonicalStaysCanonical`).
4. **The overlay check.** The changed sources are checked as an overlay through the `Checker` seam.
   - When the overlay has a diagnostic the base does not have (counted by code and message), the plan is `Blocked` by those diagnostics (`OverlayDiagnosticBlocks`), and `Apply` refuses it with `NotReady`.
   - Otherwise the plan is `Ready`. Its proof is `SameCode` when the base checks and the lowered program is unchanged (`ReformatIsSameCode`). It is `Checked` when the code changed, or when the base did not check: in that case no proof is claimed. `Alpha` belongs to rename (SEM5).
5. **The plan.** A plan holds:
   - its id: a digest of the change and of the revisions it read; the same change at the same revisions gives the same id (`PlanIsDeterministic`);
   - every file it read, with that file's revision;
   - each file it changes, with the revision before, the revision after and the new text;
   - a line diff per changed file;
   - its verdict.

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
   - The product providers wrap the Go checker and formatter, and Linux's `renameat2(RENAME_EXCHANGE)` and macOS's `renamex_np(RENAME_SWAP)`. They wait for README Gap 17, a provider that captures `go.Foreign` (SEM4b). Where no atomic exchange exists, apply is refused, with no fall-back to check-then-rename (the model's first mutant).
9. **Out of scope** (as in the model): two outside writes to one file inside one apply; a writer that keeps the old file open and writes through it later; power loss.

*Prior art:* gopls's `go_rename_symbol` returns edits and writes nothing; go fix merges fixes against one baseline and drops the ones that conflict; LSP's `WorkspaceEdit` names the document versions it was computed from; Roslyn's FixAll may merge incorrectly. Here a plan names its revisions, refuses overlaps, and checks its overlay before anything is written, so a semantic conflict is a refused plan rather than a broken tree.

## ergo code

`ergo code [--in DIR] [--json] COMMAND [ARGS...]` checks the program in `DIR` (the current folder by default), builds its fact tables and answers one question through `Reader`'s command line, which `ergo gen cli` generates ([rpc.md](rpc.md), ergo gen cli): `describe`, `symbols [--name N] [--match M] [--kind K] [--package P] [--offset O] [--limit L]`, `symbol ADDRESS`, `references --of ADDRESS [--kinds K]... [--offset O] [--limit L]`. A record answer prints as its JSON. A typed failure prints its tag and its JSON on standard error with status 1, or its tagged JSON on standard output with `--json`:

```
$ ergo code symbol Total
Ambiguous: {"_tag":"Ambiguous","address":"Total","candidates":["shop/cart.Total","shop/stock.Total"]}
```

The command line is Ergo: std `code` and its generated `codecli`, in the module `internal/linked/code/ergo`, linked into the binary as checked-in Go (docs/tooling.md, Linked Ergo packages). The tables cross from the Go checker to the Ergo reader as the JSON of `code.Tables`, a seam that capstone S1's mixed binary removes. `ergo query`, `ergo type` and `ergo graph` are gone, with no alias.

*Prior art:* Gleam's compiler records kinded references while it type-checks, and its language server's rename reads them; gopls keeps a per-package cross-reference index computed during type checking and serves `go_symbol_references` to agents by a symbol's name (`lib.T.M`); SCIP names symbols by printable strings; MoonBit's `moon ide` takes a symbol name on the command line. ReScript's editor tooling reads compiler artifacts from a second binary; here the tools are the compiler's own release.

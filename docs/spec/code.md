# Code: questions about a workspace's code

Status: `Reader` (`Describe`, `Symbols`, `Symbol`, `References`), its providers `Index` and `Scan`, and `ergo code` are implemented (SEMTOOLS SEM1). `Calls`, `Reach`, `Machine` and `Api` (SEM2), the server on `ergo dev`, MCP and HTTP (SEM3), plans and `Apply` (SEM4), rename (SEM5), patterns (SEM6) and rewrites (SEM7) are planned (`semtools/plan.md` of the wf10 run).

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

## ergo code

`ergo code [--in DIR] [--json] COMMAND [ARGS...]` checks the program in `DIR` (the current folder by default), builds its fact tables and answers one question through `Reader`'s command line, which `ergo gen cli` generates ([rpc.md](rpc.md), ergo gen cli): `describe`, `symbols [--name N] [--match M] [--kind K] [--package P] [--offset O] [--limit L]`, `symbol ADDRESS`, `references --of ADDRESS [--kinds K]... [--offset O] [--limit L]`. A record answer prints as its JSON. A typed failure prints its tag and its JSON on standard error with status 1, or its tagged JSON on standard output with `--json`:

```
$ ergo code symbol Total
Ambiguous: {"_tag":"Ambiguous","address":"Total","candidates":["shop/cart.Total","shop/stock.Total"]}
```

The command line is Ergo: std `code` and its generated `codecli`, in the module `internal/linked/code/ergo`, linked into the binary as checked-in Go (docs/tooling.md, Linked Ergo packages). The tables cross from the Go checker to the Ergo reader as the JSON of `code.Tables`, a seam that capstone S1's mixed binary removes. `ergo query`, `ergo type` and `ergo graph` are gone, with no alias.

*Prior art:* Gleam's compiler records kinded references while it type-checks, and its language server's rename reads them; gopls keeps a per-package cross-reference index computed during type checking and serves `go_symbol_references` to agents by a symbol's name (`lib.T.M`); SCIP names symbols by printable strings; MoonBit's `moon ide` takes a symbol name on the command line. ReScript's editor tooling reads compiler artifacts from a second binary; here the tools are the compiler's own release.

# Receipt: the checker's pointer-keyed maps (SELFLAYOUT SL8)

Unit SL8 of the self-layout plan (`self-layout/plan.md` §4.1, in the run folder): a counted census of the maps of `internal/sketch/check` whose key is a pointer or an interface (a node, a declaration, an object or a type), before the capstone's S3 fixes the checker's tables. For each table: how many entries it holds on a corpus, how many keys it could hold, how the code reads it, and the layout that follows (std `dense`: `Col`, `Side`, `Sparse`, or a `Pool` of short lists). Receipt only: no product code changed.

## Pins and method

- Head: `main` `e02194103`. Go 1.27.0, linux/amd64. All numbers are counts (entries, objects, probes), so they do not depend on the host's load.
- **Inventory** (static): `go/types` over the non-test files of `internal/sketch/check`; every variable of map type whose key is a pointer or an interface. Uses are classified by syntax: read by key (`m[k]` as a value), written (`m[k] = v`, `m[k]++`, `clear`), in a loop (`range m`), `delete`, `len`, and passed (the map itself assigned, returned or given to a call).
- **Census** (dynamic): every std package checked from source (50), then the `cmd/*` packages of every module under `examples/` and every package they import (264 packages checked in all). At the end of each package's check, a temporary hook (never committed) walks the checker's object graph by reflection. For each map-typed field it sums the entries over every instance met, counts the distinct keys, and records the dynamic types of the keys. Every distinct object is counted once per type; that count is the **key population** a table keyed by that type could cover. Std's objects are counted once for the whole run, a module's once per module.
- **Fill** is distinct keys over the population. For a table keyed by syntax nodes two fills are given: over every node of the corpus (327,027 nodes, comments and files aside), the fill of one column over a single node id space, and over the nodes of the kinds that occur as its keys, the fill of a column on that kind's own rows.
- The tools and the raw counts are in the run folder: `self-layout/SL8/tools/` (`static.go`, `dyn.go`, `hook.diff`, `report.ts`), `static.tsv`, `dyn.tsv`, `table.md`.

## Inventory

| Declared as | Count | Note |
| --- | ---: | --- |
| Struct fields | 149 | the tables of this receipt: 94 met on the corpus, 55 not met (below) |
| Locals | 136 | per-call scratch (seen sets, worklists); 9 of them are iterated (`range`) |
| Parameters | 35 | aliases of the tables above |
| Package-level variables | 0 | |

The plan's "397 maps" was a text count over another head. At `wf10/cs8b` the same inventory finds 594 declarations: the std snapshot codec (`export_gen.go`, CS1) adds 274 locals and parameters (`map[any]uint64` and the like in the encoder and decoder), which are not checker tables.

## Decisions (the rule, then the result)

Decided: the layout of each table follows its fill and its value — by SL8's counts (plan §2.2 "decided by a count, not by taste"); reverse by another threshold in `report.ts`, and the table regenerates:

- **Col** when more than half of the key population has an entry. For a node-keyed table, **Col on its kind's rows** when more than half of the nodes of its key kinds have one (a column over the expression rows, the identifier rows, the call rows) and less than half of all nodes.
- **Sparse** for a set (a `bool` or `struct{}` value) below half.
- **Side** for data below half.
- **Pool** for a map that sits inside another table's rows (hundreds of small instances, one per owner): a short list per owner row.
- **iterated**: the code ranges over the table, so the layout must give its entries in a stable order (dense ids are; a Go map's order is not).

| Layout | Tables |
| --- | ---: |
| Col | 8 |
| Col on its kind's rows | 10 (2 iterated) |
| Side | 58 (2 iterated) |
| Sparse | 17 |
| Pool | 1 |
| Not met on the corpus | 55 |

What this says for S3:

- The checker's big four results are per-node and mostly dense **per kind**, not over all nodes: `Info.Types` holds 34.1 % of all nodes but 50.7 % of the expression kinds that carry a type; `Info.Uses` 26.2 % of all nodes and 63.4 % of identifiers; `Info.Calls`, `Info.Matches`, `Info.Loops` hold every node of their kind. A column on the kind's rows (an id per expression, per identifier, per call) beats one column over every node.
- `Package.owners` (72,575 entries, 22 % of nodes over 47 kinds) and `Info.Defs` (18 % of identifiers) stay sparse: **Side**.
- The type interner `typeTable.of` (`Type` → `int32`, 57 % of the type objects) is already the shape of a `dense.Interner`.
- Most tables are small: 44 of the 58 Side tables and all 17 Sparse ones are below 2 % fill (the largest Side fill is 42.1 %); a hash side table costs less than a column there.
- **Not met** (55 fields): the field was nil or empty when the package's check ended. They belong to a pass's own walker (`escWalk`, `lendWalk`, `ownWalk`, `footprinter`, `inferer`, `contextPass`) or are cleared after their pass (`checker.owners`, `checker.refs`, `checker.valueRefs`). A census at the end of the check cannot count them. Known gap: a hook at the end of each pass would; S3 sizes them as per-body scratch.

## The interner on the corpus's identifiers

`dense.Strings` replayed in Go (FNV-1a, linear probing, grow to twice the slots, at least 16, when the load would pass 3/4), one table per package, over every identifier of the package's files:

| Packages | Identifiers | Distinct | Mean probes, found | Mean probes, new | Longest probe | Mean final load |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 264 | 134,353 | 23,097 | 1.65 | 2.86 | 46 | 0.55 |

A mean of 1.65 probes for a found name is near linear probing's expected 1.5 at load 0.5; the longest probe of 46 is one cluster, which FNV-1a with a power-of-two mask makes possible. No change is needed for S3; a receipt that sees the longest probe grow should try a mixed final step of the hash first.

## Tables

Read by key / written / in a loop / deleted / len / passed are use sites in the source. Instances are the map values met; entries are summed over them.

| Table (field) | Key → value | Instances | Entries | Distinct keys | Key population | Fill | Read by key / written / in a loop / deleted / len / passed | Layout |
| --- | --- | ---: | ---: | ---: | --- | ---: | --- | --- |
| `Info.Types` (info.go:168) | `syntax.Expr` → `Type` | 268 | 111617 | 111617 | 220309 19 kinds | 34.1% of nodes, 50.7% of its kinds | 67 / 2 / 1 / 0 / 0 / 71 | Col on its kind's rows, iterated |
| `Info.Uses` (info.go:176) | `*syntax.Ident` → `Object` | 268 | 85750 | 85750 | 135275 Ident | 26.2% of nodes, 63.4% of its kinds | 37 / 7 / 0 / 0 / 0 / 45 | Col on its kind's rows |
| `Package.owners` (info.go:115) | `syntax.Node` → `FactOwner` | 209 | 72575 | 72575 | 315418 47 kinds | 22.2% of nodes, 23.0% of its kinds | 1 / 1 / 0 / 0 / 0 / 4 | Side |
| `typeTable.of` (intern.go:41) | `Type` → `int32` | 268 | 46098 | 13616 | 23842 13 kinds | 57.1% | 1 / 1 / 0 / 0 / 0 / 3 | Col |
| `Info.Defs` (info.go:174) | `*syntax.Ident` → `Object` | 268 | 24803 | 24803 | 135275 Ident | 7.6% of nodes, 18.3% of its kinds | 17 / 1 / 0 / 0 / 0 / 19 | Side |
| `Info.Values` (info.go:171) | `syntax.Expr` → `constant.Value` | 268 | 17151 | 17151 | 181685 6 kinds | 5.2% of nodes, 9.4% of its kinds | 6 / 1 / 0 / 0 / 0 / 8 | Side |
| `Info.Calls` (info.go:178) | `*syntax.CallExpr` → `*Call` | 268 | 15804 | 15804 | 15804 CallExpr | 4.8% of nodes, 100.0% of its kinds | 30 / 1 / 1 / 0 / 0 / 33 | Col on its kind's rows, iterated |
| `Info.Patterns` (info.go:210) | `syntax.Expr` → `*Pattern` | 268 | 9625 | 9625 | 188552 7 kinds | 2.9% of nodes, 5.1% of its kinds | 5 / 1 / 0 / 0 / 0 / 7 | Side |
| `Info.Tails` (info.go:216) | `syntax.Stmt` → `Type` | 268 | 8300 | 8300 | 12437 ExprStmt+IfStmt+SwitchStmt+TypeSwitchStmt | 2.5% of nodes, 66.7% of its kinds | 2 / 1 / 0 / 0 / 0 / 4 | Col on its kind's rows |
| `tryCover.spine` (cover.go:25) | `*syntax.BinaryExpr` → `[]*syntax.TryExpr` | 245 | 6641 | 6641 | 10028 BinaryExpr | 2.0% of nodes, 66.2% of its kinds | 3 / 1 / 0 / 0 / 0 / 7 | Col on its kind's rows |
| `Info.Footprints` (info.go:255) | `syntax.Node` → `Footprint` | 268 | 4767 | 4767 | 19072 7 kinds | 1.5% of nodes, 25.0% of its kinds | 8 / 1 / 0 / 0 / 0 / 10 | Side |
| `Info.Implicit` (info.go:192) | `syntax.Expr` → `Conversion` | 268 | 4619 | 4619 | 179839 6 kinds | 1.4% of nodes, 2.6% of its kinds | 0 / 2 / 0 / 0 / 0 / 3 | Side |
| `checker.bodyGen` (check.go:305) | `any` → `int` | 262 | 4410 | 4410 | 4810 Func+Law+Test+pkgVar | 91.7% | 1 / 1 / 0 / 0 / 0 / 4 | Col |
| `checker.literals` (check.go:247) | `syntax.Expr` → `bool` | 193 | 3801 | 3801 | 10662 BinaryExpr+UnaryExpr | 1.2% of nodes, 35.6% of its kinds | 1 / 1 / 0 / 0 / 0 / 4 | Sparse |
| `tryCover.inner` (cover.go:23) | `*syntax.BinaryExpr` → `bool` | 245 | 3216 | 3216 | 10028 BinaryExpr | 1.0% of nodes, 32.1% of its kinds | 1 / 1 / 0 / 0 / 0 / 3 | Sparse |
| `checker.reach` (check.go:367) | `any` → `*reachNode` | 268 | 2943 | 2943 | 3603 Context+Func | 81.7% | 3 / 1 / 0 / 0 / 0 / 5 | Col |
| `typeTable.foreign` (intern.go:44) | `*Row` → `*Row` | 268 | 2780 | 531 | 21723 Row | 2.4% | 1 / 1 / 0 / 0 / 0 / 3 | Side |
| `Info.Matches` (info.go:213) | `syntax.Node` → `*Match` | 268 | 2335 | 2335 | 2335 CatchExpr+SwitchStmt+TypeSwitchStmt | 0.7% of nodes, 100.0% of its kinds | 2 / 1 / 0 / 0 / 0 / 4 | Col on its kind's rows |
| `variantIndex.first` (variantindex.go:37) | `*Named` → `int` | 608 | 2153 | 981 | 4069 Named | 24.1% | 2 / 1 / 0 / 0 / 0 / 4 | Pool (a short list per owner row) |
| `Info.Instances` (info.go:261) | `syntax.Expr` → `*Instance` | 268 | 2110 | 2110 | 15804 CallExpr | 0.6% of nodes, 13.4% of its kinds | 2 / 2 / 0 / 0 / 0 / 5 | Side |
| `checker.typeNodes` (check.go:209) | `syntax.Expr` → `bool` | 197 | 2031 | 2031 | 159522 Ident+IndexExpr+SelectorExpr | 0.6% of nodes, 1.3% of its kinds | 4 / 1 / 0 / 0 / 0 / 7 | Sparse |
| `typeTable.resolved` (intern.go:45) | `*Row` → `*Row` | 268 | 2004 | 2000 | 21723 Row | 9.2% | 1 / 1 / 0 / 0 / 0 / 3 | Side |
| `typeTable.varRows` (intern.go:56) | `*RowVar` → `*Row` | 268 | 2004 | 2000 | 2078 RowVar | 96.2% | 0 / 0 / 0 / 0 / 0 / 2 | Col |
| `checker.inferredRefs` (check.go:115) | `*syntax.Ident` → `*Func` | 268 | 1892 | 1892 | 135275 Ident | 0.6% of nodes, 1.4% of its kinds | 1 / 1 / 0 / 0 / 0 / 3 | Side |
| `checker.origins` (check.go:146) | `syntax.Node` → `syntax.Node` | 268 | 1776 | 1776 | 173822 6 kinds | 0.5% of nodes, 1.0% of its kinds | 4 / 1 / 0 / 0 / 0 / 7 | Side |
| `initDeps.deps` (initorder.go:17) | `any` → `map[any]bool` | 209 | 1522 | 1522 | 3611 Func+pkgVar | 42.1% | 6 / 2 / 0 / 1 / 0 / 14 | Side |
| `ValueRanks.rank` (tests.go:427) | `*Named` → `int` | 256 | 1307 | 428 | 4069 Named | 10.5% | 2 / 2 / 0 / 0 / 0 / 5 | Side |
| `Info.Loops` (info.go:231) | `*syntax.RangeStmt` → `*Loop` | 268 | 1253 | 1253 | 1253 RangeStmt | 0.4% of nodes, 100.0% of its kinds | 3 / 1 / 0 / 0 / 0 / 5 | Col on its kind's rows |
| `testState.sees` (tests.go:63) | `*Package` → `bool` | 67 | 1040 | 141 | 270 Package | 52.2% | 0 / 0 / 0 / 0 / 0 / 3 | Col |
| `Info.ReturnedValues` (info.go:268) | `syntax.Expr` → `bool` | 268 | 1004 | 1004 | 177981 CallExpr+CompositeLit+Ident+SelectorExpr | 0.3% of nodes, 0.6% of its kinds | 0 / 1 / 0 / 0 / 0 / 2 | Sparse |
| `checker.variants` (check.go:374) | `*syntax.VariantSpec` → `*TypeName` | 268 | 795 | 795 | 795 VariantSpec | 0.2% of nodes, 100.0% of its kinds | 1 / 1 / 0 / 0 / 0 / 3 | Col on its kind's rows |
| `checker.subjects` (check.go:352) | `any` → `*switchUnion` | 189 | 646 | 524 | 4673 Named+Union | 11.2% | 1 / 1 / 0 / 0 / 0 / 5 | Side |
| `checker.frameGen` (check.go:302) | `syntax.Node` → `int` | 262 | 638 | 638 | 9259 BlockStmt+FuncLit | 0.2% of nodes, 6.9% of its kinds | 2 / 1 / 0 / 0 / 0 / 4 | Side |
| `checker.vindex` (check.go:351) | `*Union` → `*variantIndex` | 179 | 608 | 397 | 604 Union | 65.7% | 1 / 1 / 0 / 0 / 0 / 4 | Col |
| `checker.asNames` (check.go:245) | `*Pattern` → `*syntax.Ident` | 124 | 558 | 558 | 9625 Pattern | 5.8% | 0 / 1 / 1 / 0 / 0 / 4 | Side, iterated |
| `contextOwners.of` (withown.go:67) | `*ContextEntry` → `*Context` | 113 | 472 | 471 | 471 ContextEntry | 100.0% | 2 / 1 / 0 / 0 / 0 / 6 | Col |
| `checker.contextEntries` (check.go:238) | `*syntax.ContextEntry` → `*ContextEntry` | 264 | 468 | 468 | 471 ContextEntry | 0.1% of nodes, 99.4% of its kinds | 2 / 2 / 0 / 0 / 2 / 7 | Col on its kind's rows |
| `Info.Literals` (info.go:219) | `syntax.Node` → `*Signature` | 268 | 340 | 340 | 9259 BlockStmt+FuncLit | 0.1% of nodes, 3.7% of its kinds | 6 / 3 / 2 / 0 / 0 / 12 | Side, iterated |
| `checker.mapKeysSeen` (check.go:206) | `syntax.Expr` → `bool` | 55 | 319 | 319 | 139033 Ident+IndexExpr | 0.1% of nodes, 0.2% of its kinds | 1 / 1 / 0 / 0 / 0 / 4 | Sparse |
| `Info.Withs` (info.go:233) | `*syntax.CallExpr` → `*With` | 268 | 298 | 298 | 15804 CallExpr | 0.1% of nodes, 1.9% of its kinds | 4 / 1 / 0 / 0 / 0 / 6 | Side |
| `contextOwners.seen` (withown.go:68) | `*Context` → `bool` | 113 | 286 | 285 | 285 Context | 100.0% | 1 / 1 / 0 / 0 / 0 / 4 | Col |
| `checker.contextBodies` (check.go:234) | `*syntax.ContextBody` → `*bodySum` | 264 | 285 | 285 | 285 ContextBody | 0.1% of nodes, 100.0% of its kinds | 3 / 5 / 0 / 0 / 0 / 9 | Col on its kind's rows |
| `initDeps.byVar` (initorder.go:18) | `*Var` → `*pkgVar` | 79 | 281 | 281 | 24586 Var | 1.1% | 1 / 1 / 0 / 0 / 0 / 4 | Side |
| `checker.providers` (check.go:368) | `*Named` → `[]*Named` | 268 | 266 | 54 | 4069 Named | 1.3% | 2 / 1 / 0 / 0 / 0 / 4 | Side |
| `RowVar.m` (types.go:319) | `*TypeParam` → `Type` | 135 | 259 | 69 | 562 TypeParam | 12.3% | 2 / 0 / 0 / 0 / 1 / 5 | Side |
| `vnode.vars` (variantindex.go:46) | `*TypeParam` → `*vnode` | 216 | 216 | 17 | 562 TypeParam | 3.0% | 2 / 1 / 0 / 0 / 1 / 6 | Side |
| `Info.Releases` (info.go:284) | `syntax.Node` → `[]Release` | 268 | 181 | 181 | 195111 12 kinds | 0.1% of nodes, 0.1% of its kinds | 0 / 1 / 0 / 1 / 0 / 3 | Side |
| `switchUnion.m` (switchunion.go:16) | `*TypeParam` → `Type` | 132 | 172 | 9 | 562 TypeParam | 1.6% | 2 / 0 / 0 / 0 / 0 / 8 | Side |
| `Info.Handoffs` (info.go:337) | `*syntax.Ident` → `*Var` | 268 | 116 | 116 | 135275 Ident | 0.0% of nodes, 0.1% of its kinds | 0 / 1 / 0 / 2 / 0 / 4 | Side |
| `Info.Copies` (info.go:352) | `*Func` → `[]CopySite` | 268 | 109 | 109 | 3318 Func | 3.3% | 3 / 1 / 0 / 0 / 0 / 5 | Side |
| `declKinds.kinds` (kinds.go:8) | `*TypeName` → `[]Kind` | 264 | 61 | 61 | 3629 TypeName | 1.7% | 2 / 1 / 0 / 0 / 0 / 4 | Side |
| `Info.OwnedVars` (info.go:301) | `*Var` → `*Drop` | 268 | 46 | 46 | 24586 Var | 0.2% | 0 / 1 / 0 / 0 / 0 / 2 | Side |
| `checker.guarded` (check.go:364) | `*Func` → `bool` | 268 | 40 | 40 | 3318 Func | 1.2% | 1 / 1 / 0 / 0 / 0 / 3 | Sparse |
| `Info.TryReleases` (info.go:293) | `*syntax.TryExpr` → `[]Release` | 268 | 40 | 40 | 1419 TryExpr | 0.0% of nodes, 2.8% of its kinds | 0 / 1 / 0 / 1 / 0 / 3 | Side |
| `Info.PatternOwners` (info.go:340) | `*syntax.CaseClause` → `PatternOwners` | 268 | 33 | 33 | 4579 CaseClause | 0.0% of nodes, 0.7% of its kinds | 1 / 1 / 0 / 1 / 0 / 4 | Side |
| `hostPkg.vars` (gobinding.go:233) | `*Var` → `*syntax.VarSpec` | 4 | 28 | 28 | 24586 Var | 0.1% | 1 / 1 / 0 / 0 / 0 / 3 | Side |
| `hostPkg.recvPtr` (gobinding.go:235) | `*Func` → `bool` | 4 | 22 | 22 | 3318 Func | 0.7% | 3 / 1 / 0 / 0 / 0 / 5 | Sparse |
| `checker.contextComp` (check.go:231) | `*Context` → `*contextComp` | 264 | 17 | 17 | 285 Context | 6.0% | 4 / 2 / 0 / 0 / 0 / 7 | Side |
| `Info.LocalFrames` (info.go:229) | `*syntax.FuncLit` → `bool` | 268 | 15 | 15 | 308 FuncLit | 0.0% of nodes, 4.9% of its kinds | 3 / 1 / 0 / 0 / 0 / 5 | Sparse |
| `Info.Works` (info.go:201) | `*syntax.CompositeLit` → `*WorkLit` | 6 | 14 | 14 | 6413 CompositeLit | 0.0% of nodes, 0.2% of its kinds | 0 / 1 / 0 / 0 / 0 / 3 | Side |
| `Info.Takes` (info.go:346) | `*Func` → `[]int` | 268 | 12 | 12 | 3318 Func | 0.4% | 4 / 1 / 0 / 0 / 0 / 6 | Side |
| `Info.TryCovers` (info.go:274) | `syntax.Expr` → `*syntax.TryExpr` | 268 | 10 | 10 | 171855 5 kinds | 0.0% of nodes, 0.0% of its kinds | 0 / 1 / 0 / 0 / 0 / 2 | Side |
| `tryCover.roots` (cover.go:27) | `syntax.Expr` → `[]*syntax.TryExpr` | 245 | 10 | 10 | 171855 5 kinds | 0.0% of nodes, 0.0% of its kinds | 3 / 1 / 0 / 0 / 0 / 5 | Side |
| `tryCover.right` (cover.go:30) | `*syntax.TryExpr` → `bool` | 245 | 9 | 9 | 1419 TryExpr | 0.0% of nodes, 0.6% of its kinds | 3 / 1 / 0 / 0 / 0 / 5 | Sparse |
| `tryCover.used` (cover.go:30) | `*syntax.TryExpr` → `bool` | 245 | 9 | 9 | 1419 TryExpr | 0.0% of nodes, 0.6% of its kinds | 1 / 1 / 0 / 1 / 0 / 4 | Sparse |
| `Info.Closes` (info.go:187) | `syntax.Node` → `*Close` | 268 | 7 | 7 | 39611 CallExpr+FuncDecl+SelectorExpr | 0.0% of nodes, 0.0% of its kinds | 8 / 0 / 0 / 0 / 0 / 10 | Side |
| `checker.takesSeen` (check.go:205) | `*syntax.TakesClause` → `bool` | 4 | 6 | 6 | 28 TakesClause | 0.0% of nodes, 21.4% of its kinds | 1 / 1 / 0 / 0 / 0 / 4 | Sparse |
| `checker.workSends` (check.go:122) | `*syntax.Ident` → `bool` | 4 | 6 | 6 | 135275 Ident | 0.0% of nodes, 0.0% of its kinds | 1 / 1 / 0 / 0 / 0 / 4 | Sparse |
| `Info.TaskCaptures` (info.go:321) | `syntax.Node` → `*TaskCapture` | 268 | 6 | 6 | 9259 BlockStmt+FuncLit | 0.0% of nodes, 0.1% of its kinds | 0 / 1 / 0 / 1 / 0 / 3 | Side |
| `Info.WirePairs` (info.go:181) | `*TypeName` → `*WirePair` | 3 | 3 | 1 | 3629 TypeName | 0.0% | 1 / 1 / 0 / 0 / 0 / 4 | Side |
| `Package.testBuilds` (info.go:124) | `*TypeName` → `[]*Builder` | 1 | 3 | 3 | 3629 TypeName | 0.1% | 2 / 1 / 0 / 0 / 0 / 5 | Side |
| `Info.ContextValues` (info.go:238) | `syntax.Expr` → `*With` | 268 | 2 | 2 | 135275 Ident | 0.0% of nodes, 0.0% of its kinds | 1 / 1 / 0 / 0 / 0 / 3 | Side |
| `switchUnion.rows` (switchunion.go:23) | `*vnode` → `map[int32][]*vnode` | 2 | 2 | 2 | 3413 vnode | 0.1% | 1 / 1 / 0 / 0 / 0 / 4 | Side |
| `hostPkg.variadic` (gobinding.go:236) | `*Func` → `bool` | 4 | 1 | 1 | 3318 Func | 0.0% | 1 / 1 / 0 / 0 / 0 / 3 | Sparse |
| `checker.frameInv` (check.go:303) | `syntax.Node` → `int` | 262 | 0 | 0 | 0 ? | 0.0% | 2 / 0 / 0 / 1 / 0 / 5 | Side |
| `checker.frameRef` (check.go:304) | `syntax.Node` → `int` | 262 | 0 | 0 | 0 ? | 0.0% | 1 / 1 / 0 / 0 / 0 / 4 | Side |
| `checker.inferSubjects` (check.go:157) | `any` → `*switchUnion` | 268 | 0 | 0 | 0 ? | 0.0% | 0 / 1 / 0 / 0 / 0 / 3 | Side |
| `checker.pkgPatterns` (check.go:378) | `*syntax.VarSpec` → `*pkgPattern` | 268 | 0 | 0 | 0 ? | 0.0% | 0 / 2 / 0 / 0 / 0 / 3 | Side |
| `checker.refusedMembers` (check.go:110) | `*Func` → `[2]bool` | 268 | 0 | 0 | 0 ? | 0.0% | 2 / 1 / 0 / 0 / 0 / 4 | Side |
| `checker.settled` (check.go:162) | `*Func` → `bool` | 268 | 0 | 0 | 0 ? | 0.0% | 1 / 1 / 0 / 1 / 0 / 4 | Sparse |
| `checker.synthetic` (check.go:375) | `*syntax.Ident` → `bool` | 268 | 0 | 0 | 0 ? | 0.0% | 1 / 1 / 0 / 0 / 0 / 3 | Sparse |
| `declKinds.conflicts` (kinds.go:9) | `*TypeName` → `map[string]bool` | 264 | 0 | 0 | 0 ? | 0.0% | 5 / 1 / 0 / 0 / 0 / 7 | Side |
| `hostPkg.context` (gobinding.go:237) | `*Func` → `bool` | 4 | 0 | 0 | 0 ? | 0.0% | 1 / 1 / 0 / 0 / 0 / 3 | Sparse |
| `Info.FieldTakes` (info.go:331) | `*syntax.SelectorExpr` → `FieldTake` | 268 | 0 | 0 | 0 ? | 0.0% | 0 / 1 / 0 / 0 / 0 / 2 | Side |
| `Info.ImplicitElems` (info.go:208) | `syntax.Expr` → `[]Conversion` | 268 | 0 | 0 | 0 ? | 0.0% | 0 / 1 / 0 / 0 / 0 / 2 | Side |
| `Info.OwnedTemps` (info.go:302) | `syntax.Expr` → `*Drop` | 268 | 0 | 0 | 0 ? | 0.0% | 1 / 1 / 0 / 0 / 0 / 3 | Side |
| `Info.PartHands` (info.go:309) | `syntax.Expr` → `bool` | 268 | 0 | 0 | 0 ? | 0.0% | 0 / 1 / 0 / 0 / 0 / 2 | Sparse |
| `Info.Replaced` (info.go:317) | `*syntax.CompositeLit` → `[]Replaced` | 268 | 0 | 0 | 0 ? | 0.0% | 1 / 1 / 0 / 0 / 0 / 3 | Side |
| `Info.ResultDrops` (info.go:326) | `*syntax.CallExpr` → `*Drop` | 268 | 0 | 0 | 0 ? | 0.0% | 0 / 1 / 0 / 0 / 0 / 2 | Side |
| `Info.Spreads` (info.go:197) | `*syntax.CompositeLit` → `[]Spread` | 268 | 0 | 0 | 0 ? | 0.0% | 1 / 1 / 0 / 0 / 0 / 3 | Side |
| `Info.ValueCaptures` (info.go:225) | `syntax.Node` → `[]*Var` | 268 | 0 | 0 | 0 ? | 0.0% | 1 / 1 / 0 / 0 / 0 / 3 | Side |
| `typeTable.scoped` (intern.go:50) | `Type` → `int32` | 268 | 0 | 0 | 0 ? | 0.0% | 1 / 2 / 0 / 0 / 0 / 4 | Side |
| `typeTable.scopedRows` (intern.go:51) | `*Row` → `*Row` | 268 | 0 | 0 | 0 ? | 0.0% | 1 / 2 / 0 / 0 / 0 / 4 | Side |
| `typeTable.scopedVarRows` (intern.go:52) | `*RowVar` → `*Row` | 268 | 0 | 0 | 0 ? | 0.0% | 0 / 1 / 0 / 0 / 0 / 3 | Side |
| `bodySum.embeds` (contextsum.go:227) | `*syntax.ContextEntry` → `*Context` | — | — | — | — | — | 0 / 0 / 0 / 0 / 0 / 5 | not met (transient or unused on the corpus) |
| `checker.grew` (check.go:243) | `*Func` → `[2]bool` | — | — | — | — | — | 2 / 1 / 0 / 0 / 0 / 5 | not met (transient or unused on the corpus) |
| `checker.group` (check.go:174) | `*Func` → `bool` | — | — | — | — | — | 5 / 1 / 0 / 0 / 0 / 8 | not met (transient or unused on the corpus) |
| `checker.groupValue` (check.go:175) | `*Func` → `[2]groupSite` | — | — | — | — | — | 2 / 0 / 0 / 0 / 0 / 5 | not met (transient or unused on the corpus) |
| `checker.guardedRefs` (check.go:332) | `any` → `[]guardedUse` | — | — | — | — | — | 2 / 2 / 0 / 0 / 0 / 6 | not met (transient or unused on the corpus) |
| `checker.guardTrace` (check.go:307) | `syntax.Node` → `frameVerdict` | — | — | — | — | — | 0 / 0 / 0 / 0 / 0 / 1 | not met (transient or unused on the corpus) |
| `checker.intoInvalid` (check.go:213) | `syntax.Expr` → `bool` | — | — | — | — | — | 0 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `checker.mapInstances` (check.go:380) | `*syntax.MapType` → `*syntax.IndexExpr` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 4 | not met (transient or unused on the corpus) |
| `checker.owners` (check.go:145) | `syntax.Node` → `*fileInfo` | — | — | — | — | — | 3 / 1 / 0 / 0 / 0 / 6 | not met (transient or unused on the corpus) |
| `checker.pinKept` (check.go:311) | `any` → `[]guardedUse` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 4 | not met (transient or unused on the corpus) |
| `checker.refs` (check.go:167) | `any` → `map[any]bool` | — | — | — | — | — | 3 / 1 / 1 / 0 / 0 / 7 | not met (transient or unused on the corpus) |
| `checker.refusedWheres` (check.go:386) | `*fileInfo` → `[][2]syntax.Pos` | — | — | — | — | — | 2 / 1 / 0 / 0 / 0 / 5 | not met (transient or unused on the corpus) |
| `checker.selfFinal` (check.go:267) | `*syntax.SelectorExpr` → `*Func` | — | — | — | — | — | 0 / 1 / 0 / 0 / 0 / 2 | not met (transient or unused on the corpus) |
| `checker.selfRes` (check.go:264) | `*syntax.SelectorExpr` → `*Func` | — | — | — | — | — | 2 / 2 / 0 / 0 / 0 / 9 | not met (transient or unused on the corpus) |
| `checker.useGens` (check.go:329) | `*syntax.Ident` → `[]int` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `checker.valueRefs` (check.go:168) | `any` → `map[*Func][2]useSite` | — | — | — | — | — | 4 / 1 / 0 / 0 / 0 / 7 | not met (transient or unused on the corpus) |
| `contextGroup.embeds` (contextsum.go:93) | `*syntax.ContextEntry` → `*Context` | — | — | — | — | — | 0 / 1 / 0 / 0 / 0 / 6 | not met (transient or unused on the corpus) |
| `contextPass.nodes` (contextsum.go:351) | `*Context` → `*contextNode` | — | — | — | — | — | 6 / 1 / 0 / 0 / 0 / 8 | not met (transient or unused on the corpus) |
| `escWalk.addressed` (escaping.go:118) | `*Var` → `bool` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `escWalk.aliases` (escaping.go:114) | `*Var` → `[]*Var` | — | — | — | — | — | 4 / 1 / 0 / 0 / 0 / 6 | not met (transient or unused on the corpus) |
| `escWalk.declDepth` (escaping.go:108) | `*Var` → `int` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `escWalk.events` (escaping.go:110) | `*Var` → `[]mutEvent` | — | — | — | — | — | 5 / 2 / 0 / 0 / 0 / 12 | not met (transient or unused on the corpus) |
| `escWalk.litUses` (escaping.go:126) | `*Var` → `[]litUse` | — | — | — | — | — | 3 / 2 / 0 / 0 / 0 / 6 | not met (transient or unused on the corpus) |
| `escWalk.litVar` (escaping.go:125) | `*syntax.FuncLit` → `*Var` | — | — | — | — | — | 4 / 1 / 1 / 0 / 1 / 8 | not met (transient or unused on the corpus) |
| `escWalk.localUses` (escaping.go:127) | `*Var` → `[]mutEvent` | — | — | — | — | — | 3 / 2 / 0 / 0 / 0 / 7 | not met (transient or unused on the corpus) |
| `escWalk.varFrame` (escaping.go:99) | `*Var` → `int` | — | — | — | — | — | 5 / 1 / 0 / 0 / 0 / 7 | not met (transient or unused on the corpus) |
| `escWalk.writes` (escaping.go:109) | `*syntax.Ident` → `bool` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `footprinter.nodes` (footprint.go:100) | `any` → `*fpNode` | — | — | — | — | — | 2 / 1 / 0 / 0 / 0 / 5 | not met (transient or unused on the corpus) |
| `footprinter.trimmed` (footprint.go:107) | `*Context` → `[]trimmedNode` | — | — | — | — | — | 2 / 1 / 0 / 0 / 0 / 5 | not met (transient or unused on the corpus) |
| `fpNode.caps` (footprint.go:68) | `*TypeName` → `Type` | — | — | — | — | — | 0 / 1 / 3 / 0 / 0 / 6 | not met (transient or unused on the corpus) |
| `inferer.joined` (call.go:459) | `*TypeParam` → `bool` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 4 | not met (transient or unused on the corpus) |
| `inferer.m` (call.go:455) | `*TypeParam` → `Type` | — | — | — | — | — | 11 / 11 / 0 / 0 / 0 / 34 | not met (transient or unused on the corpus) |
| `inferer.set` (call.go:456) | `*TypeParam` → `bool` | — | — | — | — | — | 3 / 2 / 0 / 0 / 0 / 9 | not met (transient or unused on the corpus) |
| `lendWalk.loans` (lend.go:33) | `syntax.Node` → `[]resourceLoan` | — | — | — | — | — | 4 / 1 / 0 / 1 / 0 / 7 | not met (transient or unused on the corpus) |
| `lendWalk.parent` (lend.go:31) | `syntax.Node` → `syntax.Node` | — | — | — | — | — | 10 / 1 / 0 / 0 / 0 / 12 | not met (transient or unused on the corpus) |
| `lendWalk.points` (lend.go:32) | `*Var` → `lendPoint` | — | — | — | — | — | 5 / 1 / 0 / 0 / 0 / 7 | not met (transient or unused on the corpus) |
| `mentioner.seen` (authority.go:343) | `Type` → `bool` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `monoState.bad` (mono.go:34) | `*TypeParam` → `bool` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 4 | not met (transient or unused on the corpus) |
| `monoState.reported` (mono.go:35) | `*TypeParam` → `bool` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `operand.self` (expr.go:50) | `*TypeParam` → `Type` | — | — | — | — | — | 0 / 0 / 0 / 0 / 2 / 8 | not met (transient or unused on the corpus) |
| `ownWalk.borrow` (ownership.go:234) | `*Var` → `*Var` | — | — | — | — | — | 2 / 1 / 0 / 0 / 0 / 5 | not met (transient or unused on the corpus) |
| `ownWalk.copies` (ownership.go:231) | `*TypeParam` → `CopySite` | — | — | — | — | — | 2 / 1 / 0 / 0 / 0 / 6 | not met (transient or unused on the corpus) |
| `ownWalk.loopMemo` (ownership.go:216) | `syntax.Node` → `*ownLoopMemo` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `ownWalk.slot` (ownership.go:207) | `*Var` → `int` | — | — | — | — | — | 11 / 2 / 0 / 1 / 0 / 15 | not met (transient or unused on the corpus) |
| `patResolver.own` (match.go:109) | `*syntax.Ident` → `bool` | — | — | — | — | — | 1 / 0 / 0 / 0 / 0 / 2 | not met (transient or unused on the corpus) |
| `refWalker.argRows` (refs.go:89) | `syntax.Node` → `[2]bool` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `refWalker.binders` (refs.go:112) | `*syntax.Ident` → `bool` | — | — | — | — | — | 0 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `refWalker.callees` (refs.go:86) | `syntax.Node` → `bool` | — | — | — | — | — | 2 / 1 / 0 / 0 / 0 / 4 | not met (transient or unused on the corpus) |
| `refWalker.idents` (refs.go:110) | `*syntax.Ident` → `any` | — | — | — | — | — | 0 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `refWalker.nameIdents` (refs.go:111) | `*syntax.Ident` → `bool` | — | — | — | — | — | 0 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `refWalker.refs` (refs.go:80) | `any` → `bool` | — | — | — | — | — | 0 / 2 / 0 / 0 / 0 / 4 | not met (transient or unused on the corpus) |
| `refWalker.undecided` (refs.go:113) | `*syntax.Ident` → `bool` | — | — | — | — | — | 0 / 1 / 0 / 0 / 0 / 3 | not met (transient or unused on the corpus) |
| `refWalker.values` (refs.go:84) | `*Func` → `[2]useSite` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 4 | not met (transient or unused on the corpus) |
| `refWalker.visited` (refs.go:114) | `syntax.Node` → `bool` | — | — | — | — | — | 0 / 1 / 0 / 0 / 0 / 4 | not met (transient or unused on the corpus) |
| `testState.stray` (tests.go:54) | `*syntax.AssertStmt` → `bool` | — | — | — | — | — | 1 / 1 / 0 / 0 / 0 / 4 | not met (transient or unused on the corpus) |

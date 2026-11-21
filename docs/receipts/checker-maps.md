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
- **Not met** (55 fields): the field was nil or empty when the package's check ended. They belong to a pass's own walker (`escWalk`, `lendWalk`, `ownWalk`, `footprinter`, `inferer`, `contextPass`) or are cleared after their pass (`checker.owners`, `checker.refs`, `checker.valueRefs`). A census at the end of the check cannot count them. Known gap: a hook at the end of each pass would; S3 sizes them as per-body scratch. Section C0a below counts them, and the locals.

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

## C0a: the transient tables and the locals (capstone §11)

Unit C0a of the self-hosting capstone (`selfhost/capstone.md` §11, in the run folder) counts the two gaps above: the 55 fields that were nil at the end of a package's check, and the 136 locals. Receipt only: no product code changed. The rows give S3's table design (C0c) the size and the lifetime of each scratch table.

### Pins and method

- Head: `main` `d3bb9dac6`, one census run. Go 1.27.0, linux/amd64. All numbers are counts, so they do not depend on the host's load.
- Corpus: the same as SL8's. Every std package checked from source (50), then the `cmd/*` packages of every module under `examples/` and their imports: 267 packages checked in all (264 at SL8's head).
- Inventory at this head (SL8's `static.go`, `export_gen.go` left out because it is the std snapshot codec): 166 fields, 151 locals, 36 parameters. Since SL8's head the checker has 17 new fields (2 of them in the codec's encoder, also left out) and 15 new locals. A row marked "(new since SL8)" is one of them. At this head 64 fields are nil at the end of a check: SL8's 55, 7 new checker fields, and the 2 encoder fields.
- Census points. Temporary instrumentation (never committed; it is rewritten into the source by a tool, then the source is restored) records the size of a tracked map at each point where its size can fall or where it stops being reachable:
  - at the end of each of the 42 passes of `runOnce`, for the checker's own fields and the walker values they hold;
  - just before a statement that assigns, clears or deletes from a tracked field or local (`x.f = …`, `clear(x.f)`, `delete(m, k)`);
  - at the exit of the scope that declares a local map or a local walker (`escWalk`, `lendWalk`, `ownWalk`, `refWalker`, `inferer`, `footprinter`, …), by a deferred call.

  A map only shrinks at a delete, a clear or a reassignment, so the largest size seen at these points is the map's peak. One map value is one **instance**. **Entries** are the sum of the instances' peaks. **Distinct keys** are counted over the whole run, as in SL8, and the **key population** and the fill are SL8's (the objects of each type met by the end-of-package walk at this head). A key population of 0 means the keys are objects that the package's graph does not hold at the end (for example a body node of the check).
- **Lifetime**: "inside a pass" is a checker field that is made and dropped inside one pass (resets counts how often); "walker" is a field of a value that one call builds and drops; "reset per use" is a walker field that the walker remakes; "never made" is a field that is nil at every census point (the owner type's sightings are given).
- The tools and raw counts are in the run folder, `selfhost/C0a/tools/` (`instrument/`, `census/`, `hook.ts`, `rebuild.sh`, `report.ts`) with `census.tsv`, `static.tsv`, `instrument.tsv`, `table.md`.

### The rule (SL8's, with two classes for scratch)

Decided: the same count decides, with two added classes for tables that live inside a pass, by SL8's rule (plan §2.2 "decided by a count") and simplicity (a class only where the count tells it apart); reverse by changing `report.ts` and regenerating:

- **Col** when the distinct keys cover more than half of their kinds' population. **Col, epoch-stamped** when such a table sets each key more than twice over the run: a column that a call resets by a generation number, not by clearing.
- **Scratch** for many small instances (more than 300, a mean below 50 entries): one reused buffer per walker, cleared between uses.
- **Sparse** (a set) and **Side** (data) for the rest, as in SL8.
- **alias** when more than half of the instances are also seen under another table's name (a parameter or a field passed in): not a table of its own.
- **none** when the table has no entry on the corpus.

| Layout | Fields (62) | Locals (151) |
| --- | ---: | ---: |
| Col | 7 | 17 |
| Col, epoch-stamped | 2 | 5 |
| Scratch | 17 | 19 |
| Sparse | 0 | 19 |
| Side | 5 | 17 |
| alias | 2 | 26 |
| none (no entry on the corpus) | 29 | 48 |

| Field lifetime | Fields |
| --- | ---: |
| never made on the corpus | 26 |
| walker (one per call) | 20 |
| walker, reset per use | 8 |
| inside a pass | 8 |

### What this says for S3

- Measured: the transient tables are as large as the package-end tables. The 62 fields make 199,929 map instances with 554,563 entries over the corpus; the 151 locals make 175,608 instances with 198,087 entries. SL8's largest package-end table, `Info.Types`, holds 111,617.
- Measured: one table is most of it. `lendWalk.parent` (a node's parent) is built once per function body: 7,156 instances, 423,362 entries, 242,180 distinct nodes of 327,027 (74.1 %). Inferred: a parent column built once per package removes the 1.75 rebuilds and the map; it is the biggest single saving in the checker's tables that this census finds.
- Measured: the `Var`-keyed walker tables `escWalk.varFrame` and `escWalk.declDepth` cover 63.8 % of all `Var`s (16,329 each). Inferred: a column on the package's `Var` table.
- Measured: generic inference makes 41,747 `inferer` values, each with two maps: `set` holds 1.05 entries on average and `m` 0.06, 7 at most; `inferer.set` sets each type parameter key 109 times over the run. Inferred: an epoch-stamped column on the `TypeParam` table, or a fixed array of the callee's type parameters, instead of two maps per call; this is the most frequent map allocation in the checker.
- Measured: the 36 Scratch tables make 134,319 instances with 80,190 entries, a mean of 0.6 entries per map, and the largest instance of any of them has 43 entries (`escWalk.writes`). Inferred: one buffer per walker, reused (a `dense` `Stack` cleared per use), covers them all; most of today's maps are made and dropped empty.
- Measured: 26 locals are aliases (85,264 instances), for example `paramMap:m` and `selfBinding:m`, which pass a type parameter substitution on. They need no table of their own.
- Measured: 29 fields and 48 locals hold no entry on the corpus: error paths (`checker.intoInvalid`, `checker.refusedWheres`), traces (`checker.guardTrace`, `refWalker`'s trace sets), and code that no example reaches (`mentioner`, `keepWalk`). S3 sizes them from their code, not from this corpus.

Known gaps: the peak is measured at the census points, so a delete, clear or reassignment outside a statement list (in a `for` header) is not a census point, and a map that shrinks there is under-counted. Distinct keys of a std object met in several modules count once, as in SL8. Instances count map values, not calls: a local that stays nil is not an instance.

Pinned order: one count moved between runs. The largest instance of `ValueRanks.Rank`'s two locals was 18 in one run and 19 in another, because `ungeneratableFrom` ranges over its `seen` set in Go map order, and that order decides which `Rank` call solves the largest open set. The census build visits that set in type-name order (`censusSortedNamed`, part of the uncommitted hook), and the table gives that order's value, 17. With the pin, three census runs at this head print byte-identical counts (`census.tsv`), and two runs print byte-identical SL8 walks (`dyn.tsv`, with ties in its object list sorted by name). No other count depends on map order on this corpus.

Read by key / written / in a loop / deleted / len / passed are use sites in the source at this head, as in SL8's tables.

### C0a: Fields that were nil at the end of a package's check

| Table (field) | Key → value | Lifetime | Instances | Entries (sum of peaks) | Largest | Distinct keys | Key population | Fill | Layout |
| --- | --- | --- | ---: | ---: | ---: | ---: | --- | ---: | --- |
| `lendWalk.parent` (lend.go:31) | `ergo.local/prototype/internal/sketch/syntax.Node` → `ergo.local/prototype/internal/sketch/syntax.Node` | walker (one per call) | 7156 | 423362 | 912 | 242180 | 327027 45 kinds | 74.1% | Col |
| `inferer.set` (call.go:456) | `*TypeParam` → `bool` | walker (one per call) | 41747 | 43672 | 7 | 401 | 563 TypeParam | 71.2% | Col, epoch-stamped (each key set 109 times) |
| `escWalk.declDepth` (escaping.go:108) | `*Var` → `int` | walker (one per call) | 653 | 16329 | 248 | 16329 | 25577 Var | 63.8% | Col |
| `escWalk.varFrame` (escaping.go:99) | `*Var` → `int` | walker (one per call) | 653 | 16329 | 248 | 16329 | 25577 Var | 63.8% | Col |
| `lendWalk.points` (lend.go:32) | `*Var` → `lendPoint` | walker (one per call) | 7156 | 16041 | 42 | 9131 | 25577 Var | 35.7% | Scratch |
| `footprinter.nodes` (footprint.go:100) | `any` → `*fpNode` | walker (one per call) | 267 | 4957 | 133 | 4957 | 20307 8 kinds | 24.4% | Side |
| `refWalker.callees` (refs.go:86) | `ergo.local/prototype/internal/sketch/syntax.Node` → `bool` | walker (one per call) | 3345 | 4820 | 32 | 4820 | 140923 Ident | 3.4% | Scratch |
| `checker.refs` (check.go:193) | `any` → `map[any]bool` | inside a pass (267 resets) | 267 | 3345 | 115 | 3345 | 3742 Func+pkgVar | 89.4% | Col |
| `checker.valueRefs` (check.go:194) | `any` → `map[*Func][2]useSite` | inside a pass (267 resets) | 267 | 3345 | 115 | 3345 | 3742 Func+pkgVar | 89.4% | Col |
| `refWalker.refs` (refs.go:80) | `any` → `bool` | walker (one per call) | 3345 | 3257 | 12 | 2045 | 3742 Func+pkgVar | 54.6% | alias (another table's map) |
| `checker.group` (check.go:200) | `*Func` → `bool` | inside a pass (3324 resets) | 3324 | 3048 | 4 | 3048 | 3445 Func | 88.5% | Col |
| `ownWalk.loopMemo` (ownership.go:219) | `ergo.local/prototype/internal/sketch/syntax.Node` → `*ownLoopMemo` | walker (one per call) | 10441 | 2433 | 6 | 1364 | 1411 ForStmt+RangeStmt | 96.7% | Col |
| `inferer.m` (call.go:455) | `*TypeParam` → `Type` | walker (one per call) | 41747 | 2376 | 7 | 394 | 563 TypeParam | 70.0% | Col, epoch-stamped (each key set 6 times) |
| `escWalk.writes` (escaping.go:109) | `*ergo.local/prototype/internal/sketch/syntax.Ident` → `bool` | walker (one per call) | 653 | 1962 | 43 | 1962 | 140923 Ident | 1.4% | Scratch |
| `fpNode.caps` (footprint.go:68) | `*TypeName` → `Type` | walker (one per call) | 1082 | 1800 | 7 | 53 | 3678 TypeName | 1.4% | Scratch |
| `operand.self` (expr.go:50) | `*TypeParam` → `Type` | walker (one per call) | 1291 | 1561 | 2 | 52 | 563 TypeParam | 9.2% | alias (another table's map) |
| `refWalker.values` (refs.go:84) | `*Func` → `[2]useSite` | walker (one per call) | 3345 | 1407 | 9 | 800 | 3445 Func | 23.2% | Scratch |
| `escWalk.events` (escaping.go:110) | `*Var` → `[]mutEvent` | walker, reset per use (876 resets) | 876 | 1336 | 10 | 1336 | 25577 Var | 5.2% | Scratch |
| `checker.selfRes` (check.go:303) | `*ergo.local/prototype/internal/sketch/syntax.SelectorExpr` → `*Func` | inside a pass (47 resets) | 47 | 1141 | 165 | 1141 | 21517 SelectorExpr | 5.3% | Side |
| `ownWalk.slot` (ownership.go:210) | `*Var` → `int` | walker, reset per use (922 resets) | 10441 | 795 | 9 | 466 | 25577 Var | 1.8% | Scratch |
| `inferer.joined` (call.go:459) | `*TypeParam` → `bool` | walker (one per call) | 309 | 650 | 4 | 135 | 563 TypeParam | 24.0% | Scratch |
| `ownWalk.copies` (ownership.go:234) | `*TypeParam` → `CopySite` | walker (one per call) | 3049 | 166 | 4 | 165 | 563 TypeParam | 29.3% | Scratch |
| `refWalker.argRows` (refs.go:89) | `ergo.local/prototype/internal/sketch/syntax.Node` → `[2]bool` | walker (one per call) | 3345 | 136 | 7 | 136 | 320 FuncLit | 42.5% | Scratch |
| `escWalk.addressed` (escaping.go:118) | `*Var` → `bool` | walker, reset per use (6739 resets) | 7392 | 81 | 2 | 81 | 25577 Var | 0.3% | Scratch |
| `checker.groupValue` (check.go:201) | `*Func` → `[2]groupSite` | inside a pass (3324 resets) | 3324 | 58 | 4 | 58 | 3445 Func | 1.7% | Scratch |
| `checker.pinKept` (check.go:350) | `any` → `[]guardedUse` | inside a pass (3324 resets) | 3324 | 58 | 4 | 58 | 3445 Func | 1.7% | Scratch |
| `checker.guardedRefs` (check.go:371) | `any` → `[]guardedUse` | inside a pass (267 resets) | 267 | 20 | 2 | 20 | 3742 Func+pkgVar | 0.5% | Side |
| `contextPass.nodes` (contextsum.go:351) | `*Context` → `*contextNode` | walker (one per call) | 114 | 18 | 2 | 18 | 286 Context | 6.3% | Side |
| `escWalk.litUses` (escaping.go:132) | `*Var` → `[]litUse` | walker, reset per use (6739 resets) | 7392 | 17 | 4 | 17 | 25577 Var | 0.1% | Scratch |
| `escWalk.litVar` (escaping.go:131) | `*ergo.local/prototype/internal/sketch/syntax.FuncLit` → `*Var` | walker, reset per use (6739 resets) | 7392 | 17 | 4 | 17 | 320 FuncLit | 5.3% | Scratch |
| `escWalk.localUses` (escaping.go:133) | `*Var` → `[]mutEvent` | walker, reset per use (6739 resets) | 7392 | 15 | 3 | 15 | 25577 Var | 0.1% | Scratch |
| `lendWalk.loans` (lend.go:33) | `ergo.local/prototype/internal/sketch/syntax.Node` → `[]resourceLoan` | walker, reset per use (287 resets) | 7156 | 10 | 2 | 5 | 9647 BlockStmt+FuncLit | 0.1% | Scratch |
| `footprinter.trimmed` (footprint.go:107) | `*Context` → `[]trimmedNode` | walker (one per call) | 1 | 1 | 1 | 1 | 286 Context | 0.3% | Side |
| `checker.grew` (check.go:282) | `*Func` → `[2]bool` | inside a pass (3324 resets) | 3324 | 0 | 0 | 0 | 0 ? | — | none (no entry on the corpus) |
| `escWalk.aliases` (escaping.go:114) | `*Var` → `[]*Var` | walker, reset per use (6739 resets) | 7392 | 0 | 0 | 0 | 0 ? | — | none (no entry on the corpus) |
| `escWalk.borrowHolders` (escaping.go:121) (new since SL8) | `*Var` → `Type` | walker (one per call) | 653 | 0 | 0 | 0 | 0 ? | — | none (no entry on the corpus) |
| `bodySum.embeds` (contextsum.go:227) | `*ergo.local/prototype/internal/sketch/syntax.ContextEntry` → `*Context` | never made (owner seen 8303 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.guardTrace` (check.go:346) | `ergo.local/prototype/internal/sketch/syntax.Node` → `frameVerdict` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.intoInvalid` (check.go:252) | `ergo.local/prototype/internal/sketch/syntax.Expr` → `bool` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.lateEdges` (check.go:121) (new since SL8) | `*ergo.local/prototype/internal/sketch/syntax.FuncDecl` → `[]*ergo.local/prototype/internal/sketch/syntax.FuncDecl` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.mapInstances` (check.go:419) | `*ergo.local/prototype/internal/sketch/syntax.MapType` → `*ergo.local/prototype/internal/sketch/syntax.IndexExpr` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.owners` (check.go:171) | `ergo.local/prototype/internal/sketch/syntax.Node` → `*fileInfo` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.refusedWheres` (check.go:425) | `*fileInfo` → `[][2]ergo.local/prototype/internal/sketch/syntax.Pos` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.relHit` (check.go:114) (new since SL8) | `ergo.local/prototype/internal/sketch/syntax.Node` → `bool` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.relSeeds` (check.go:113) (new since SL8) | `ergo.local/prototype/internal/sketch/syntax.Node` → `relSeed` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.selfFinal` (check.go:306) | `*ergo.local/prototype/internal/sketch/syntax.SelectorExpr` → `*Func` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.useGens` (check.go:368) | `*ergo.local/prototype/internal/sketch/syntax.Ident` → `[]int` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `checker.wireEdges` (check.go:121) (new since SL8) | `*ergo.local/prototype/internal/sketch/syntax.FuncDecl` → `[]*ergo.local/prototype/internal/sketch/syntax.FuncDecl` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `contextGroup.embeds` (contextsum.go:93) | `*ergo.local/prototype/internal/sketch/syntax.ContextEntry` → `*Context` | never made (owner seen 628 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `escWalk.keeps` (escaping.go:124) (new since SL8) | `*Func` → `map[*TypeParam]string` | never made (owner seen 1207 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `keepWalk.declared` (borrowtargs.go:92) (new since SL8) | `*Var` → `int` | never made (owner not built on the corpus) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `mentioner.seen` (authority.go:343) | `Type` → `bool` | never made (owner not built on the corpus) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `monoState.bad` (mono.go:34) | `*TypeParam` → `bool` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `monoState.reported` (mono.go:35) | `*TypeParam` → `bool` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `ownWalk.borrow` (ownership.go:237) | `*Var` → `*Var` | never made (owner seen 10441 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `patResolver.own` (match.go:109) | `*ergo.local/prototype/internal/sketch/syntax.Ident` → `bool` | never made (owner seen 4384 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `refWalker.binders` (refs.go:112) | `*ergo.local/prototype/internal/sketch/syntax.Ident` → `bool` | never made (owner seen 10035 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `refWalker.idents` (refs.go:110) | `*ergo.local/prototype/internal/sketch/syntax.Ident` → `any` | never made (owner seen 10035 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `refWalker.nameIdents` (refs.go:111) | `*ergo.local/prototype/internal/sketch/syntax.Ident` → `bool` | never made (owner seen 10035 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `refWalker.undecided` (refs.go:113) | `*ergo.local/prototype/internal/sketch/syntax.Ident` → `bool` | never made (owner seen 10035 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `refWalker.visited` (refs.go:114) | `ergo.local/prototype/internal/sketch/syntax.Node` → `bool` | never made (owner seen 10035 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |
| `testState.stray` (tests.go:54) | `*ergo.local/prototype/internal/sketch/syntax.AssertStmt` → `bool` | never made (owner seen 11214 times, field nil) | 0 | 0 | 0 | 0 | — | — | none (no entry on the corpus) |

### C0a: Locals

| Local (function:name) | Key → value | Instances | Aliased | Entries (sum) | Largest | Distinct keys | Key population | Fill | Read by key / written / in a loop / deleted / len / passed | Layout |
| --- | --- | ---: | ---: | ---: | ---: | ---: | --- | ---: | --- | --- |
| `*checker.finishInfo:memo` (info.go:1341) | `Type` → `Type` | 267 | 0 | 21904 | 460 | 16064 | 138952 11 kinds | 11.6% | 0 / 0 / 0 / 0 / 0 / 1 | Side |
| `lawClosures:s` (laws.go:646) | `*Package` → `bool` | 4190 | 2481 | 20795 | 30 | 267 | 273 Package | 97.8% | 0 / 1 / 0 / 0 / 0 / 3 | alias (another table's map) |
| `typeArgBorrow:seen` (borrowconv.go:71) (new since SL8) | `Type` → `bool` | 10120 | 0 | 13550 | 7 | 4654 | 23135 10 kinds | 20.1% | 1 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `paramMap:m` (expr.go:213) | `*TypeParam` → `Type` | 6944 | 6706 | 10465 | 7 | 541 | 563 TypeParam | 96.1% | 0 / 1 / 0 / 0 / 0 / 2 | alias (another table's map) |
| `lawClosures:s` (laws.go:643) | `*Package` → `bool` | 2134 | 2134 | 9160 | 19 | 122 | 273 Package | 44.7% | 0 / 0 / 0 / 0 / 0 / 1 | alias (another table's map) |
| `*ownWalk.letSpec:loans` (ownership.go:928) | `*Var` → `[]resourceLoan` | 8096 | 0 | 7906 | 1 | 4578 | 25577 Var | 17.9% | 2 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*checker.wireEach:seen` (wirecall.go:387) | `Type` → `bool` | 1219 | 0 | 7516 | 29 | 167 | 11763 7 kinds | 1.4% | 1 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*checker.closeFacts:selections` (close.go:97) | `*SelectorExpr` → `*Close` | 267 | 0 | 6750 | 240 | 6750 | 21517 SelectorExpr | 31.4% | 1 / 1 / 0 / 0 / 0 / 1 | Side |
| `*checker.recordOwnership:m` (resource.go:474) | `*TypeParam` → `Type` | 4526 | 4526 | 6658 | 7 | 537 | 563 TypeParam | 95.4% | 0 / 0 / 0 / 0 / 0 / 1 | alias (another table's map) |
| `*checker.reportInitCycles:index` (initorder.go:111) | `any` → `int` | 355 | 0 | 5304 | 116 | 3595 | 3742 Func+pkgVar | 96.1% | 4 / 1 / 0 / 0 / 0 / 1 | Col |
| `*checker.reportInitCycles:low` (initorder.go:112) | `any` → `int` | 355 | 0 | 5304 | 116 | 3595 | 3742 Func+pkgVar | 96.1% | 4 / 3 / 0 / 0 / 0 / 3 | Col |
| `*checker.reportInitCycles:on` (initorder.go:113) | `any` → `bool` | 355 | 0 | 5304 | 116 | 3595 | 3742 Func+pkgVar | 96.1% | 1 / 2 / 0 / 0 / 0 / 2 | Col |
| `lawClosures:out` (laws.go:640) | `*Package` → `map[*Package]bool` | 348 | 267 | 4190 | 30 | 267 | 273 Package | 97.8% | 1 / 1 / 0 / 0 / 0 / 2 | alias (another table's map) |
| `*checker.instanceWitnesses:m` (info.go:622) | `*TypeParam` → `Type` | 2157 | 2157 | 3481 | 7 | 446 | 563 TypeParam | 79.2% | 0 / 0 / 0 / 0 / 0 / 1 | alias (another table's map) |
| `*checker.recordDep:m` (initorder.go:50) | `any` → `bool` | 1652 | 52 | 3337 | 12 | 2025 | 3742 Func+pkgVar | 54.1% | 0 / 1 / 0 / 0 / 0 / 4 | Col |
| `selfBinding:m` (typeops.go:642) | `*TypeParam` → `Type` | 30976 | 30976 | 3218 | 2 | 56 | 563 TypeParam | 9.9% | 0 / 1 / 0 / 0 / 0 / 2 | alias (another table's map) |
| `*checker.checkBodies:byFunc` (rows.go:212) | `*Func` → `*bodyNode` | 267 | 0 | 3048 | 106 | 3048 | 3445 Func | 88.5% | 0 / 1 / 0 / 0 / 0 / 3 | Col |
| `*checker.inferTakes:by` (takes.go:195) | `*Func` → `*bodyNode` | 267 | 0 | 3048 | 106 | 3048 | 3445 Func | 88.5% | 1 / 1 / 0 / 0 / 0 / 1 | Col |
| `*typeTable.varRow:memo` (intern.go:153) | `*RowVar` → `*Row` | 232 | 0 | 3042 | 104 | 2135 | 2187 RowVar | 97.6% | 1 / 1 / 0 / 0 / 0 / 2 | Col |
| `*checker.lawRuns:seen` (laws.go:453) | `*Package` → `bool` | 267 | 267 | 2947 | 30 | 267 | 273 Package | 97.8% | 0 / 0 / 1 / 0 / 1 / 0 | alias (another table's map) |
| `*checker.lawRuns:sees` (laws.go:452) | `*Package` → `map[*Package]bool` | 267 | 267 | 2947 | 30 | 267 | 273 Package | 97.8% | 2 / 0 / 0 / 0 / 0 / 0 | alias (another table's map) |
| `*checker.inferTakes:seen` (takes.go:206) | `*bodyNode` → `bool` | 3048 | 0 | 2652 | 11 | 1629 | 0 bodyNode | keys outside the package graph | 1 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*Named.instanceUnder:m` (decl.go:1701) | `*TypeParam` → `Type` | 1452 | 0 | 2389 | 7 | 95 | 563 TypeParam | 16.9% | 0 / 1 / 0 / 0 / 0 / 2 | Scratch |
| `*checker.ungeneratableFrom:seen` (tests.go:233) | `*Named` → `bool` | 2126 | 0 | 2277 | 19 | 444 | 4203 Named | 10.6% | 1 / 1 / 1 / 0 / 0 / 1 | Scratch |
| `*checker.checkRecursiveStructs:indices` (entry.go:40) | `*Named` → `int` | 267 | 0 | 2161 | 62 | 2119 | 4203 Named | 50.4% | 1 / 1 / 0 / 0 / 0 / 1 | Col |
| `*checker.witnessInstance:seen` (info.go:571) | `*Func` → `[]*entry` | 2169 | 0 | 2157 | 1 | 206 | 3445 Func | 6.0% | 2 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*checker.checkRecursiveStructs:declIndex` (entry.go:42) | `*Named` → `int` | 267 | 0 | 2110 | 61 | 2110 | 4203 Named | 50.2% | 3 / 1 / 0 / 0 / 0 / 1 | Col |
| `*checker.discoverInit:m` (rows.go:292) | `any` → `bool` | 1700 | 52 | 1870 | 12 | 1183 | 3742 Func+pkgVar | 31.6% | 0 / 1 / 0 / 0 / 0 / 4 | Scratch |
| `*checker.discoverInit:refs` (rows.go:291) | `any` → `bool` | 1700 | 1700 | 1870 | 12 | 1183 | 3742 Func+pkgVar | 31.6% | 0 / 0 / 1 / 0 / 0 / 0 | alias (another table's map) |
| `*refWalker.pattern:binds` (refs.go:572) | `*Ident` → `bool` | 4066 | 0 | 1865 | 3 | 1865 | 140923 Ident | 1.3% | 1 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*checker.selfSig:m` (typeops.go:608) | `*TypeParam` → `Type` | 19938 | 19938 | 1609 | 2 | 56 | 563 TypeParam | 9.9% | 1 / 0 / 0 / 0 / 1 / 1 | alias (another table's map) |
| `eachTypeParam:seen` (takes.go:170) | `Type` → `bool` | 1048 | 0 | 1595 | 7 | 393 | 11825 6 kinds | 3.3% | 1 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*checker.selfCall:self` (expr.go:1386) | `*TypeParam` → `Type` | 1291 | 1291 | 1561 | 2 | 52 | 563 TypeParam | 9.2% | 1 / 0 / 0 / 0 / 0 / 3 | alias (another table's map) |
| `*checker.contextSource:owners` (contextsum.go:108) | `*FuncDecl` → `*Named` | 267 | 0 | 1361 | 68 | 1361 | 3445 FuncDecl | 39.5% | 2 / 1 / 0 / 0 / 0 / 1 | Side |
| `*checker.lawRuns:providers` (laws.go:462) | `*Named` → `[]LawProvider` | 267 | 0 | 1355 | 13 | 61 | 4203 Named | 1.5% | 1 / 1 / 0 / 0 / 0 / 1 | Side |
| `*ValueRanks.Rank:cur` (tests.go:473) | `*Named` → `int` | 2139 | 0 | 1345 | 17 | 432 | 4203 Named | 10.3% | 4 / 2 / 0 / 0 / 0 / 2 | Scratch |
| `*ValueRanks.Rank:inOpen` (tests.go:442) | `*Named` → `bool` | 2139 | 0 | 1345 | 17 | 432 | 4203 Named | 10.3% | 1 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*inferer.mentions:seen` (unify.go:53) | `*Named` → `bool` | 3496 | 0 | 1226 | 3 | 118 | 4203 Named | 2.8% | 1 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*checker.checkRefinements:laws` (refine.go:411) | `*Field` → `bool` | 653 | 0 | 1219 | 35 | 1219 | 15014 Field | 8.1% | 1 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*checker.checkInstantiationCycles:comp` (mono.go:226) | `*TypeParam` → `int` | 838 | 0 | 1045 | 123 | 373 | 563 TypeParam | 66.3% | 4 / 1 / 0 / 0 / 0 / 1 | Col, epoch-stamped (each key set 3 times) |
| `*checker.checkInstantiationCycles:index` (mono.go:223) | `*TypeParam` → `int` | 838 | 0 | 1045 | 123 | 373 | 563 TypeParam | 66.3% | 4 / 1 / 0 / 0 / 0 / 1 | Col, epoch-stamped (each key set 3 times) |
| `*checker.checkInstantiationCycles:low` (mono.go:224) | `*TypeParam` → `int` | 838 | 0 | 1045 | 123 | 373 | 563 TypeParam | 66.3% | 4 / 3 / 0 / 0 / 0 / 3 | Col, epoch-stamped (each key set 3 times) |
| `*checker.checkInstantiationCycles:on` (mono.go:225) | `*TypeParam` → `bool` | 838 | 0 | 1045 | 123 | 373 | 563 TypeParam | 66.3% | 1 / 2 / 0 / 0 / 0 / 2 | Col, epoch-stamped (each key set 3 times) |
| `*checker.checkInstantiationCycles:out` (mono.go:210) | `*TypeParam` → `[]int` | 838 | 0 | 1045 | 123 | 373 | 563 TypeParam | 66.3% | 3 / 2 / 0 / 0 / 0 / 2 | Col, epoch-stamped (each key set 3 times) |
| `*Package.IsErrorType:set` (owners.go:74) | `*Named` → `bool` | 210 | 3 | 935 | 17 | 162 | 4203 Named | 3.9% | 1 / 0 / 0 / 0 / 0 / 0 | Sparse |
| `*checker.subst:newOf` (typeops.go:91) | `*Method` → `*Method` | 1887 | 0 | 830 | 8 | 42 | 1133 Method | 3.7% | 1 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*resWalk.visit:sub` (resource.go:196) | `*TypeParam` → `Type` | 407 | 0 | 697 | 4 | 16 | 563 TypeParam | 2.8% | 0 / 1 / 0 / 0 / 0 / 4 | Scratch |
| `*checker.addErrorType:set` (owners.go:121) | `*Named` → `Expr` | 202 | 202 | 628 | 17 | 299 | 4203 Named | 7.1% | 1 / 1 / 0 / 0 / 0 / 4 | alias (another table's map) |
| `*checker.publishErrorTypes:out` (owners.go:147) | `*Named` → `bool` | 202 | 3 | 628 | 17 | 299 | 4203 Named | 7.1% | 0 / 1 / 0 / 0 / 0 / 2 | Sparse |
| `*checker.publishErrorTypes:set` (owners.go:146) | `*Named` → `Expr` | 202 | 202 | 628 | 17 | 299 | 4203 Named | 7.1% | 0 / 0 / 1 / 0 / 1 / 0 | alias (another table's map) |
| `*checker.errorTypeIn:set` (owners.go:135) | `*Named` → `Expr` | 184 | 184 | 602 | 17 | 295 | 4203 Named | 7.0% | 1 / 0 / 0 / 0 / 0 / 0 | alias (another table's map) |
| `*checker.acquire:built` (withown.go:230) | `*ContextEntry` → `bool` | 347 | 0 | 529 | 7 | 465 | 472 ContextEntry | 98.5% | 1 / 1 / 0 / 0 / 0 / 1 | Col |
| `*checker.acquire:want` (withown.go:205) | `*ContextEntry` → `bool` | 347 | 0 | 529 | 7 | 465 | 472 ContextEntry | 98.5% | 1 / 1 / 1 / 0 / 1 / 1 | Col |
| `*checker.withOwnership:owners` (withown.go:94) | `*ContextEntry` → `*Context` | 114 | 0 | 472 | 18 | 472 | 472 ContextEntry | 100.0% | 1 / 0 / 0 / 0 / 0 / 2 | Col |
| `*checker.mapKeyCopies:owner` (takes.go:248) | `*TypeParam` → `*Func` | 267 | 0 | 466 | 152 | 466 | 563 TypeParam | 82.8% | 1 / 1 / 0 / 0 / 0 / 2 | Col |
| `*checker.kindBracketParams:free` (decl.go:2185) | `*TypeParam` → `bool` | 3301 | 0 | 392 | 7 | 392 | 563 TypeParam | 69.6% | 2 / 1 / 0 / 0 / 1 / 1 | Col |
| `*checker.kindBracketParams:seen` (decl.go:2192) | `*TypeParam` → `Kind` | 170 | 0 | 392 | 7 | 392 | 563 TypeParam | 69.6% | 1 / 2 / 0 / 0 / 0 / 2 | Col |
| `occurs:seen` (mono.go:127) | `*Named` → `bool` | 2652 | 0 | 361 | 2 | 61 | 4203 Named | 1.5% | 1 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*ownWalk.replaced:seen` (ownership.go:1948) | `*Field` → `bool` | 251 | 0 | 317 | 3 | 103 | 4223 Field | 2.4% | 2 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.subst:nm` (typeops.go:60) | `*TypeParam` → `Type` | 158 | 0 | 299 | 7 | 69 | 563 TypeParam | 12.3% | 0 / 1 / 0 / 0 / 0 / 5 | Side |
| `*checker.checkBodies:byVar` (rows.go:213) | `*pkgVar` → `*bodyNode` | 267 | 0 | 297 | 11 | 297 | 297 pkgVar | 100.0% | 0 / 1 / 0 / 0 / 0 / 3 | Col |
| `*checker.recordInitOrder:order` (initorder.go:257) | `*pkgVar` → `int` | 267 | 0 | 297 | 11 | 297 | 297 pkgVar | 100.0% | 1 / 1 / 0 / 0 / 0 / 1 | Col |
| `*checker.subjectUnion:subjects` (switchunion.go:41) | `any` → `*switchUnion` | 84 | 84 | 292 | 14 | 278 | 4839 Named+Union | 5.7% | 1 / 1 / 0 / 0 / 0 / 2 | alias (another table's map) |
| `*checker.planLawContexts:seen` (lawcontexts.go:49) | `*Package` → `bool` | 14 | 14 | 203 | 22 | 70 | 273 Package | 25.6% | 0 / 0 / 1 / 0 / 0 / 0 | alias (another table's map) |
| `*checker.pinGroup:uses` (rows.go:477) | `*Func` → `[2]useSite` | 58 | 58 | 169 | 8 | 122 | 3445 Func | 3.5% | 0 / 0 / 0 / 0 / 0 / 1 | alias (another table's map) |
| `*checker.collectSelfFuncs:owners` (decl.go:2290) | `any` → `bool` | 64 | 0 | 161 | 13 | 64 | 4203 Named | 1.5% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*dropWalk.drain:sub` (drop.go:123) | `*TypeParam` → `Type` | 60 | 0 | 110 | 2 | 3 | 563 TypeParam | 0.5% | 0 / 1 / 0 / 0 / 1 / 2 | Side |
| `*checker.recordInitOrder:seen` (initorder.go:282) | `any` → `bool` | 297 | 0 | 107 | 14 | 101 | 3742 Func+pkgVar | 2.7% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.dropNeeds:seen` (releaseneeds.go:113) (new since SL8) | `*Drop` → `bool` | 69 | 0 | 76 | 2 | 76 | 53 Drop | 100.0% (keys outside the graph) | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.running:seen` (running.go:29) | `*Drop` → `bool` | 50 | 0 | 62 | 3 | 62 | 53 Drop | 100.0% (keys outside the graph) | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.solveDeclKinds:isDecl` (kinds.go:165) | `*TypeName` → `bool` | 267 | 0 | 61 | 14 | 61 | 3678 TypeName | 1.7% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.converge:in` (rows.go:789) | `*bodyNode` → `bool` | 37 | 0 | 58 | 4 | 58 | 0 bodyNode | keys outside the package graph | 2 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.converge:preds` (rows.go:793) | `*bodyNode` → `[]*bodyNode` | 37 | 0 | 58 | 4 | 58 | 0 bodyNode | keys outside the package graph | 2 / 1 / 0 / 0 / 0 / 1 | Side |
| `*checker.planLawContexts:runs` (lawcontexts.go:45) | `*Law` → `int` | 14 | 0 | 54 | 15 | 45 | 728 Law | 6.2% | 1 / 2 / 0 / 0 / 0 / 2 | Side |
| `*ownWalk.literalCaptures:seen` (lend.go:535) | `*Var` → `bool` | 287 | 0 | 46 | 2 | 16 | 25577 Var | 0.1% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.discoverInit:visited` (rows.go:303) | `*Func` → `bool` | 88 | 0 | 38 | 15 | 38 | 3445 Func | 1.1% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `ownTypeParam:bound` (typeops.go:630) | `*TypeParam` → `Type` | 9723 | 9723 | 24 | 2 | 6 | 563 TypeParam | 1.1% | 1 / 0 / 0 / 0 / 0 / 0 | alias (another table's map) |
| `receiverTypeArgs:bound` (info.go:663) | `*TypeParam` → `Type` | 24 | 24 | 24 | 2 | 6 | 563 TypeParam | 1.1% | 1 / 0 / 0 / 0 / 0 / 0 | alias (another table's map) |
| `*Builder.Instance:m` (builders.go:68) | `*TypeParam` → `Type` | 23 | 23 | 23 | 1 | 1 | 563 TypeParam | 0.2% | 0 / 0 / 0 / 0 / 0 / 1 | alias (another table's map) |
| `*checker.rewriteShape:param` (rewrite.go:98) (new since SL8) | `*Var` → `int` | 11 | 0 | 20 | 2 | 20 | 25577 Var | 0.1% | 2 / 1 / 0 / 0 / 0 / 2 | Side |
| `*checker.checkContextCycles:index` (reach.go:62) | `*Context` → `int` | 14 | 0 | 18 | 2 | 18 | 286 Context | 6.3% | 4 / 1 / 0 / 0 / 0 / 1 | Side |
| `*checker.checkContextCycles:low` (reach.go:63) | `*Context` → `int` | 14 | 0 | 18 | 2 | 18 | 286 Context | 6.3% | 4 / 3 / 0 / 0 / 0 / 3 | Side |
| `*checker.checkContextCycles:on` (reach.go:64) | `*Context` → `bool` | 14 | 0 | 18 | 2 | 18 | 286 Context | 6.3% | 1 / 2 / 0 / 0 / 0 / 2 | Sparse |
| `*checker.checkContextCycles:succ` (reach.go:56) | `*Context` → `[]*Context` | 14 | 0 | 18 | 2 | 18 | 286 Context | 6.3% | 2 / 1 / 0 / 0 / 0 / 1 | Side |
| `*ownWalk.frameCaptured:oldSlots` (ownership.go:582) | `*Var` → `int` | 7751 | 0 | 18 | 2 | 6 | 25577 Var | 0.0% | 0 / 2 / 1 / 0 / 0 / 2 | Scratch |
| `*checker.closeFacts:facts` (close.go:49) | `Node` → `*Close` | 267 | 0 | 7 | 2 | 7 | 41485 CallExpr+FuncDecl+SelectorExpr | 0.0% | 0 / 2 / 0 / 0 / 0 / 2 | Side |
| `*checker.completeDefinedTypes:done` (shapes.go:228) | `*Named` → `bool` | 267 | 0 | 6 | 2 | 6 | 4203 Named | 0.1% | 1 / 2 / 0 / 0 / 0 / 2 | Sparse |
| `*checker.joinInstance:set` (join.go:188) | `*TypeParam` → `bool` | 4 | 4 | 6 | 2 | 3 | 563 TypeParam | 0.5% | 0 / 1 / 0 / 0 / 0 / 2 | alias (another table's map) |
| `prelude:coll` (check.go:717) | `Object` → `bool` | 1 | 0 | 5 | 5 | 5 | 3445 Func | 0.1% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.callGeneric:defaults` (call.go:698) | `*TypeParam` → `Type` | 1178 | 0 | 3 | 1 | 1 | 563 TypeParam | 0.2% | 2 / 1 / 0 / 0 / 0 / 1 | Scratch |
| `*checker.literalType:m` (expr.go:800) | `*TypeParam` → `Type` | 3 | 3 | 3 | 1 | 2 | 563 TypeParam | 0.4% | 2 / 0 / 0 / 0 / 0 / 1 | alias (another table's map) |
| `*checker.literalType:set` (expr.go:801) | `*TypeParam` → `bool` | 3 | 3 | 3 | 1 | 2 | 563 TypeParam | 0.4% | 1 / 1 / 0 / 0 / 0 / 2 | alias (another table's map) |
| `*checker.lawInstance:m` (laws.go:548) | `*TypeParam` → `Type` | 2 | 0 | 2 | 1 | 1 | 563 TypeParam | 0.2% | 0 / 1 / 0 / 0 / 0 / 2 | Side |
| `*checker.literalType:byLiteral` (expr.go:809) | `*TypeParam` → `Type` | 3 | 0 | 2 | 1 | 1 | 563 TypeParam | 0.2% | 1 / 1 / 0 / 0 / 0 / 1 | Side |
| `*checker.checkUnfoldCycles:byOwner` (rewrite.go:278) (new since SL8) | `*TypeName` → `[]*Law` | 267 | 0 | 1 | 1 | 1 | 3678 TypeName | 0.0% | 3 / 1 / 0 / 0 / 0 / 1 | Side |
| `*checker.checkUnfoldCycles:next` (rewrite.go:291) (new since SL8) | `*Method` → `[]*Method` | 1 | 0 | 1 | 1 | 1 | 1133 Method | 0.1% | 3 / 1 / 0 / 0 / 0 / 1 | Side |
| `*checker.checkUnfoldCycles:seen` (rewrite.go:300) (new since SL8) | `*Method` → `bool` | 1 | 0 | 1 | 1 | 1 | 1133 Method | 0.1% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.collectSelfFuncs:typeOwners` (decl.go:2264) (new since SL8) | `*Named` → `bool` | 267 | 0 | 1 | 1 | 1 | 4203 Named | 0.0% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.contextSource:locals` (contextsum.go:122) | `*ContextLit` → `*Context` | 1 | 1 | 1 | 1 | 1 | 268 ContextLit | 0.4% | 1 / 0 / 0 / 0 / 0 / 0 | alias (another table's map) |
| `*checker.contextsReached:seenContext` (reach.go:130) | `*Context` → `bool` | 18 | 0 | 1 | 1 | 1 | 286 Context | 0.3% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.scanLocals:locals` (context_local.go:69) | `*ContextLit` → `*Context` | 1 | 1 | 1 | 1 | 1 | 268 ContextLit | 0.4% | 0 / 1 / 0 / 0 / 0 / 4 | alias (another table's map) |
| `*contextPass.components:imported` (contextsum.go:523) | `*Context` → `bool` | 18 | 0 | 1 | 1 | 1 | 286 Context | 0.3% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*contextPass.components:succ` (contextsum.go:522) | `*contextComp` → `bool` | 18 | 0 | 1 | 1 | 1 | 18 contextComp | 5.6% | 1 / 1 / 0 / 0 / 0 / 1 | Sparse |
| `*checker.checkBodies:pending` (rows.go:232) | `any` → `[]pendingSelf` | 267 | 0 | 0 | 0 | 0 | 0 ? | — | 0 / 1 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.checkCells:seen` (cells.go:313) (new since SL8) | `Node` → `bool` | 267 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.checkContextCycles:cyclic` (reach.go:67) | `*Context` → `bool` | 14 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.checkErrorMethods:blamed` (entry.go:742) | `*Named` → `bool` | 267 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.checkImportAliases:refused` (decl.go:642) | `*ImportSpec` → `bool` | 267 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.checkWireNeeds:done` (wirecall.go:350) (new since SL8) | `Node` → `bool` | 267 | 0 | 0 | 0 | 0 | 0 ? | — | 2 / 2 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.completeDefinedTypes:active` (shapes.go:228) | `*Named` → `bool` | 267 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 2 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.completeDefinedTypes:pending` (shapes.go:229) | `*Named` → `bool` | 267 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.contextsReached:seenFunc` (reach.go:129) | `*Func` → `bool` | 18 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.releaseSeeds:replaced` (releaseneeds.go:58) (new since SL8) | `Expr` → `bool` | 267 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.releaseSeeds:seeds` (releaseneeds.go:47) (new since SL8) | `Node` → `relSeed` | 267 | 267 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.reportGoCopyWrites:held` (gocopy.go:18) | `*Var` → `bool` | 4591 | 0 | 0 | 0 | 0 | 0 ? | — | 2 / 1 / 0 / 0 / 1 / 1 | none (no entry on the corpus) |
| `*checker.reportGoCopyWrites:operand` (gocopy.go:19) | `*Ident` → `bool` | 4591 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.run:seeds` (decl.go:24) (new since SL8) | `Node` → `relSeed` | 267 | 267 | 0 | 0 | 0 | 0 ? | — | 0 / 0 / 0 / 0 / 1 / 1 | none (no entry on the corpus) |
| `*checker.withOwnership:refused` (withown.go:106) | `*ContextEntry` → `bool` | 267 | 0 | 0 | 0 | 0 | 0 ? | — | 2 / 2 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.withOwnership:taskHeld` (withown.go:107) | `*CallExpr` → `bool` | 267 | 0 | 0 | 0 | 0 | 0 ? | — | 0 / 0 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*escWalk.finishCaptures:reported` (escaping.go:780) | `*Ident` → `bool` | 1336 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 2 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*escWalk.finishCaptures:seen` (escaping.go:781) | `Node` → `bool` | 1336 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*ownWalk.replaced:copied` (ownership.go:1916) | `*Field` → `bool` | 251 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `zeroAuthority:seen` (authority.go:73) | `*Struct` → `bool` | 14 | 0 | 0 | 0 | 0 | 0 ? | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.contextSource:embeds` (contextsum.go:122) | `*ContextEntry` → `*Context` | 0 | 0 | 0 | 0 | 0 | — | — | 0 / 0 / 1 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.expandIncludes:again` (include.go:63) | `*Named` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.externInstance:m` (extern.go:406) | `*TypeParam` → `Type` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 4 | none (no entry on the corpus) |
| `*checker.instantiateValue:m` (expr.go:226) | `*TypeParam` → `Type` | 0 | 0 | 0 | 0 | 0 | — | — | 0 / 0 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.lawContext:m` (lawcontexts.go:98) | `*TypeParam` → `Type` | 0 | 0 | 0 | 0 | 0 | — | — | 0 / 1 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.methodOf:m` (typeops.go:566) | `*TypeParam` → `Type` | 0 | 0 | 0 | 0 | 0 | — | — | 0 / 1 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.needEdges:out` (wirecall.go:331) (new since SL8) | `*FuncDecl` → `[]*FuncDecl` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 4 | none (no entry on the corpus) |
| `*checker.originFile:seen` (owner.go:148) | `Node` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.ownedBlock:closed` (withown.go:439) | `*ContextEntry` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 2 / 2 / 0 / 0 / 0 / 3 | none (no entry on the corpus) |
| `*checker.pkgPatternCheck:byName` (patterns.go:245) | `*Ident` → `*pkgVar` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.pkgPatternCheck:own` (patterns.go:246) | `*Ident` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 0 / 1 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.reportedUses:uses` (refs.go:504) | `*Func` → `[2]useSite` | 0 | 0 | 0 | 0 | 0 | — | — | 0 / 0 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.reportGoCopyWrites:ergoUse` (gocopy.go:55) | `*Var` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.reportGroup:sites` (rows.go:651) | `*Func` → `[2]groupSite` | 0 | 0 | 0 | 0 | 0 | — | — | 3 / 0 / 0 / 0 / 0 / 3 | none (no entry on the corpus) |
| `*checker.run:edges` (decl.go:24) (new since SL8) | `*FuncDecl` → `[]*FuncDecl` | 0 | 0 | 0 | 0 | 0 | — | — | 0 / 0 / 0 / 0 / 1 / 1 | none (no entry on the corpus) |
| `*checker.scanLocals:embeds` (context_local.go:70) | `*ContextEntry` → `*Context` | 0 | 0 | 0 | 0 | 0 | — | — | 0 / 1 / 0 / 0 / 0 / 4 | none (no entry on the corpus) |
| `*checker.settleWaiting:needed` (selfcalls.go:167) | `*bodyNode` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 3 / 2 / 1 / 0 / 1 / 2 | none (no entry on the corpus) |
| `*checker.settleWaiting:preds` (selfcalls.go:168) | `*bodyNode` → `[]*bodyNode` | 0 | 0 | 0 | 0 | 0 | — | — | 3 / 2 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.skipFrame:rec` (provisional.go:205) | `Node` → `int` | 0 | 0 | 0 | 0 | 0 | — | — | 0 / 1 / 0 / 0 / 0 / 2 | none (no entry on the corpus) |
| `*checker.usesIn:memo` (refs.go:520) | `*guardNode` → `frameState` | 0 | 0 | 0 | 0 | 0 | — | — | 0 / 0 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `*checker.usesIn:out` (refs.go:515) | `*Func` → `[2]useSite` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 4 | none (no entry on the corpus) |
| `*escWalk.keepsTParam:m` (borrowtargs.go:63) (new since SL8) | `*TypeParam` → `string` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 0 / 0 / 0 / 0 / 0 | none (no entry on the corpus) |
| `Authorities:seen` (authority.go:271) | `Type` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `Buildable:idx` (authority.go:176) | `Type` → `int` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `Constructible:seen` (authority.go:109) | `Type` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `GoImports:seen` (goinspect.go:17) | `*Package` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `pendingDefinition:seen` (shapes.go:178) | `Type` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |
| `zeroMade:seen` (mint.go:309) | `Type` → `bool` | 0 | 0 | 0 | 0 | 0 | — | — | 1 / 1 / 0 / 0 / 0 / 1 | none (no entry on the corpus) |

# The graph rows (`ergo graph export`)

The checker's graph of a program as dense rows (CI-SELECT §12, GRAPH-TABLE). Every tool that asks what depends on what (the affected selector, the `layout` lint pack, a project's own workflow through std `codegraph`) reads these rows; none keeps a second analysis. Built by `internal/sketch/graphrows` from what the loader and the checker already resolved: the import specs, SEM's fact tables (`internal/sketch/facts`), the checker's law subjects and tests, and the verdict store's digester (`lower.RunTarget.Inputs`, `lower.TestInputs`).

```
ergo graph export [DIR|DIR/...] [--json]
```

The queries over these rows are std `codegraph`'s functions, and `ergo graph cycles | why | affected | hidden | chains | fan | interfaces | files | directories | field-writes | test-support | seams | folder-cycles | layers` their command line ([stdlib, Graph and codegraph](stdlib.md#graph-and-codegraph); `ergo graph --help`).

`DIR/...` takes every package below `DIR`: of `DIR`'s module, or, at a workspace root (an `ergo.work`, or no module holds `DIR`), of every member ([build](build.md#workspace)). At a workspace root the rows hold a `module` node for every member, also one with no package, and a `requires` edge for each member another resolves locally.

## Nodes

Each node has an id (its index), a kind, a sub-kind, a parent, a name, and for some a digest and a subject.

| Kind | Sub | Parent | Name | Digest |
| --- | --- | --- | --- | --- |
| `module` | | none | module path, or `PATH@VERSION` | |
| `package` | `go` for `import go` | its module; none outside the program (std) | import path | |
| `file` | | its package | module-relative path | |
| `decl` | `func type let law test machine context operation variant field` | its file; a member's declaration; a std declaration's package | handle within the package: `Name`, `Type.Member`, `Name#test`, `Name#law` | a top-level declaration's token digest (no comment, space or position) |
| `task` | `test law-provider law-context law-machine` | its package | the report's label | the verdict store's key (`RunTarget.Digest`; for a test, the same rule over its declaration) |

A task's `subject` is the test's or law's declaration (an owner's or machine's for a member law).

## Edges

| Kind | From -> to |
| --- | --- |
| `contains` | module > package > file > top-level declaration > member |
| `imports` | file -> package, at the import path |
| `uses` | declaration -> declaration, at the name; `how` is the fact site's kind (`call`, `read`, `write`, `typeuse`, `construct`, `match`, `needs`, `fails`, `provides`, `binds`, `handles`) |
| `needs` | function -> capability of its derived needs row |
| `fails` | function -> failure of its derived error row |
| `provides` | declaration -> capability a context it holds binds or provides |
| `reexports` | `let X = q.Y` -> the declaration `Y` |
| `testof` | test -> each declaration of its package it names |
| `lawof` | law -> its subjects (a top-level law's, `Law.Subjects`), or its type or machine |
| `input` | task -> each declaration whose tokens its key covers |
| `requires` | module -> workspace module it resolves locally: a `replace` line of its `go.mod` whose folder is the other's (at a workspace root only) |

Locals, parameters and type parameters are no nodes: their sites stay inside one declaration. A name the program does not import a package for (a predeclared one) is no node either.

## Rules

- Positions (`file`, `offset`, `line`, `column`) and a file node's physical line count (`lines`, 0 for non-file nodes; version 2) are side columns no key reads: a comment or layout edit changes them and nothing else.
- With `--json` every row has every field, so a reader with no optional fields (std json into `codegraph.Rows`) decodes it: a missing node (a root's parent, a non-task's subject, a row with no position's file) is `-1`, and a row with no position has offset, line and column 0.
- A task's key changes if and only if the tokens of a declaration its `input` rows name change (`TestGraphRowsAreTheCheckedProgram`).
- A package that does not check gives the rows the checker resolved and no tasks.
- The rows are made when asked; nothing caches them yet (no count of repeat readers).

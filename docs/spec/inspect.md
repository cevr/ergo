# Public API inspection

Status: **implemented**. The text output is also the docs view until a separate doc generator exists. Inspection checks source; it does not run the program or lower it to Go.

```
ergo inspect api FILE|DIR [--json]
ergo inspect api --diff OLD NEW [--json]
```

1. The view lists each exported function and method in name order. A
   function reached as a method is qualified by its receiver type. It shows
   its checked signature, `fails`, `needs`, its written `takes`
   clause, and the own marks of its type parameters: each `[T own]` says
   `own T (a resource may bind it)`; a type parameter without the mark
   binds plain data only (types.md, Generics 9; MODES-5). Inspection reads
   the mark as written; it does not infer another one.
2. The summary diff reports added, removed and changed contracts. Takes
   and own marks are API facts, not implementation-only details. A newly
   taken parameter or a dropped own mark says **breaks callers that pass a
   resource**. Removing those restrictions does not carry that
   warning. Moving an existing copy site's location changes its docs but
   does not introduce a new restriction. A plain-data signature change is
   still an API change, without this resource warning.
3. JSON lists the same functions or changes, including both old and new
   contracts and `BreaksResourceCallers`. Both inputs of a diff must check.
   Exit 0 means inspection succeeded (even if there are API changes), 1 is
   a source or operational error, and 2 is an invalid invocation.

4. `ergo inspect facts FILE|DIR [--json]` ([facts.md](facts.md), Lowering
   7) also prints a `fact-go-reads` row at each Go call operand that a
   foreign fact of the binding names ([interop.md](interop.md#foreign-facts)):
   position, the Go function, `reads p`, the level (`checked` for a derived
   fact) and its source, and what the lowering did with it. The JSON row
   adds `law` (the guarding law of a vouched fact) and `use`.

Evidence: `TestOwnershipAPIModel` generates checked packages and edits
(copy, take, export, plain-data signature); its independent model decides
which contracts change and which newly restrict resource callers.
`TestInspectAPIRoute` checks the CLI route, text and JSON in process.

Prior art (inferred from PRIOR_ART.md's pinned sources): Gleam projects
typed module metadata into docs and host declarations; ReScript's `.resi`
and generated host declarations expose checked public shapes; Elixir
keeps documentation/typespec metadata beside BEAM exports. Those languages
copy values freely and do not expose an affine takes contract. This view
uses Ergo's checker facts, not a second source analyzer. It adds no runtime
code and changes no lowering or optimization path.

## Forms

Status: **implemented** for the passes on the line today: the memo, merge and fold passes (IR-FORMS IRF-6). Unlike the API view, this view lowers the program to the IR in process; it runs no Go tool.

```
ergo inspect forms FILE|DIR [--json]
```

1. The view lists each decision a pass over the run build's IR made at a
   source site, by position: the form the pass looked at, the pass,
   `fired` or `not fired`, the Go name of the body, and in parentheses what
   the pass did or why it did not, in the pass's own words. A site of a
   body the IR path does not build (it falls back to the text emitter) is
   not listed: no pass ran there. A site of a std body is std's, not the
   program's, and is not listed either. With no site the view prints
   `no form site`.
2. Each pass gives its reasons from one table (`lower.FormReasons`); a pass
   that names another is an internal error. The view, these docs and the
   test read that table.
3. The memo pass (form `call`; LAWFFI LF3a) computes a call once per entry
   of its loop and reads the value after. It looks at each call of a
   function that is the whole value of a let, an assignment or an argument
   inside a loop. Its reasons:
   - fired: `computed once per entry of its loop`;
   - `the callee is not pure`: the callee is not an Ergo function lowered
     pure, a pure std body or a reviewed-pure Go function;
   - `a method, a task, a call with a block or a call site`;
   - `an operand or the result is not a number, a bool or a string`;
   - `an operand may change in the loop`: an operand is not a literal, a
     constant or a name bound before the loop that no assignment reaches;
   - `statements run before the call`: an operand runs statements of its
     own (an effectful call, an if);
   - `the call folds to a constant`: the fold pass computes it at compile
     time, or it calls a function of no operands whose body is one such
     call (which Go then inlines as a constant), so it needs no cache.

   A call inside an operator's operand (`acc + sq(k)`) is not looked at
   yet: the forms substrate's statement slot gives the pass those sites.

   The merge pass (form `call`; LAWFFI LF3b) runs on the built body: a
   pure call equal to one whose value a let bound earlier (the same callee,
   the same operands, each a constant or a name) reads that let's value.
   It looks at each pure call that has an equal call before it. Its
   reasons:
   - fired: `reads the value of an equal call before it`;
   - `an operand or the earlier value is written in the function, or not of a basic type`:
     a name involved is a `let mut`, or its address is taken, or it is not
     a number, a bool or a string;
   - `the equal call before it is not a whole value`: the earlier call is
     an operator's operand, so no name holds its value;
   - `the equal call before it does not run first on every path`: the
     earlier let is in a branch or a loop body this call is outside of,
     or a jump target lies between them.

   The fold pass (form `call`; LAWFFI LF3c) runs on the built body before
   the merge pass. A call of a reviewed-pure Go function that has a
   check-time native (`strconv.Quote`, `QuoteToASCII`, `Itoa`,
   `FormatBool`, `strings.ToUpper`, `ToLower`, `TrimSpace`,
   `filepath.Clean`) on constants becomes the constant the compiler
   computes with the same Go function. A let bound once to a constant,
   or to a folded call, is a constant operand. Some of these functions read
   Go's Unicode tables, so the pass folds only when the Go that builds the
   program is the pinned release (`ergo version`) and the compiler runs on
   that release. The Go that builds it is `ERGO_GO`'s choice: the managed
   release, or for `system` or a path the release that go command reports
   (`go env GOVERSION`); a go command that does not say folds nothing. It
   looks at each such call.
   Its reasons:
   - fired: `computed at compile time by the same Go function`;
   - `an operand is not a constant of a basic type`: a parameter, a
     `let mut`, an interpolation or any other value the IR does not hold
     as a constant;
   - `the Go that builds the program is not the Go the compiler runs on`.
4. `--json` lists the same sites with `file`, `line`, `column`, `func`,
   `form`, `pass`, `fired` and `reason`. Exit 0 when the package checks, 1
   when it does not, 2 on an invalid invocation.

Evidence: `TestInspectFormsRows` (`cmd/ergo`) holds a program per reason
of the table, its text line and its JSON site, and that this section names
each reason.

Prior art: Koka's `fip` and `fbip` make an optimization a checked promise
the compiler reports on; GHC's `-ddump-rule-firings` and
`-ddump-simpl-stats` name the rewrites that fired. Elixir, Gleam and
ReScript report nothing of their host's optimizer: their speed is the
host's. This view reports Ergo's own passes, decided before Go's compiler,
so a user sees why a site did not get faster.

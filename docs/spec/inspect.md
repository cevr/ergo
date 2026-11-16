# Public API inspection

Status: **implemented**. The text output is also the docs view until a separate doc generator exists. Inspection checks source; it does not run the program or lower it to Go.

```
ergo inspect api FILE|DIR [--json]
ergo inspect api --diff OLD NEW [--json]
```

1. The view lists each exported function and method in name order. A
   function reached as a method is qualified by its receiver type. It shows
   its checked signature, `fails`, `needs`, its written `takes`
   clause, and the checker's copy policy for its generic type parameters.
   Each body copy says `copies T at file:line:column (plain data only)` and
   gives the checker's reason. A bodiless generic says `foreign body` in
   place of a location: it may copy each parameter unless the reviewed
   runtime table lists it as moving only. Inspection queries that same
   checker policy; it does not infer another one. No copy mark is written
   in source.
2. The summary diff reports added, removed and changed contracts. Takes
   and copies are API facts, not implementation-only details. A newly taken
   parameter or newly copying type parameter says **breaks callers that
   pass a resource**. Removing those restrictions does not carry that
   warning. Moving an existing copy site's location changes its docs but
   does not introduce a new restriction. A plain-data signature change is
   still an API change, without this resource warning.
3. JSON lists the same functions or changes, including both old and new
   contracts and `BreaksResourceCallers`. Both inputs of a diff must check.
   Exit 0 means inspection succeeded (even if there are API changes), 1 is
   a source or operational error, and 2 is an invalid invocation.

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

Status: **implemented** for the passes on the line today: the memo pass (IR-FORMS IRF-6). Unlike the API view, this view lowers the program to the IR in process; it runs no Go tool.

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
     own (an effectful call, an if).

   A call inside an operator's operand (`acc + sq(k)`) is not looked at
   yet: the forms substrate's statement slot gives the pass those sites.
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

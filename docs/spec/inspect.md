# Public API inspection

Status: **implemented** (L8 C3). The text output is also the docs view until
a separate doc generator exists. Inspection checks source; it does not run
the program or lower it to Go.

```
ergo inspect api FILE|DIR [--json]
ergo inspect api --diff OLD NEW [--json]
```

1. The view lists each exported function and method in name order. A
   function reached as a method is qualified by its receiver type. It shows
   its checked signature, `fails`, `needs`, its written or inferred `takes`
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

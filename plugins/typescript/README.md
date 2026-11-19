# plugins/typescript

work's TypeScript language plugin, level 1 (SEMWS WG4): a `Splitter` by [oxc-parser](https://oxc.rs/docs/guide/usage/parser) that speaks work's plugin protocol (`modules/work/work/plugin.ergo`). It is the one folder of SEMWS that is not Ergo: it has its own test command and is not in the Go gate.

```toml
# work.toml
[language.typescript]
command = "bun plugins/typescript/main.js"
files = ["*.ts", "*.tsx", "*.mts", "*.cts"]
```

After `work trust`, `work merge-file auto` (the Git driver `work init` sets) merges these files by declarations, and `work plugin-test typescript` admits the plugin on the repository's own files.

## Units

A unit is one top-level statement (`split.js`), keyed by the names it declares:

- an overload set (the declarations of one function in a row) is one unit; `export default` and `export =` are the key `default`;
- an import is keyed by its module (`import ./x`), its named specifiers are its members, so imports merge as a set;
- `declare module "m"`, `namespace N` and `declare global` are one unit each, their statements its members;
- a statement that declares nothing is keyed by what it calls and the string it gives first (`code test "adds"`), or by its kind (`code IfStatement`); never by its offset;
- a key taken twice (interface merging) keys the later ones `I #2`.

Members are given for classes, interfaces, enums, object literals, modules and specifiers. A unit runs from the line after the statement before it to the line after its own end, so the comment above a statement belongs to its unit and the comment after it on its line stays with it. A member runs from the end of the member before it, so its separator leads it. `.tsx` parses as TSX, the others as TypeScript.

Every other request is `NotProvided`: level 2 (`Changes`, `Sites`, `Check`) is WG5's, by TypeScript's own server.

## Tests

- `bun test`: the Splitter's laws (`UnitsAreTheFile`, `KeysAreUnique`, members inside their unit) on a corpus of TypeScript shapes; `EditStaysInItsUnit` on seeded edits (`SEED=N SEEDS=1 bun test` replays one); a row for each of the plan's faults (a key by position, two overloads as two units, a lost leading comment, a .tsx file read as .ts, a `declare module` block cut in two) and the protocol.
- `bun mutants.js`: each row of `mutants.json` made in a copy of this folder must fail `bun test`.
- `work plugin-test -max 0 -files '*.ts,*.tsx,*.mts,*.cts' "bun plugins/typescript/main.js"` on a repository: the laws on every file (`docs/receipts/work-typescript.md`).

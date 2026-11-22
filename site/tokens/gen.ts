// gen.ts writes the tokens file of every TypeScript and Go snippet under
// site/examples: `<file>.tokens.json` beside `<file>`, the records of
// @pierre/highlights (pinned in package.json) with the snippet's digest.
// The format and the rule that turns records into tokens are in
// site/snippets/tokens.ergo; the snippet guard fails when a snippet
// changes and its tokens file is not written again. Run from the
// repository root, after `bun install --frozen-lockfile` in site/tokens:
//
//	bun site/tokens/gen.ts
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { LiveTokenizer, tokenNames } from "@pierre/highlights";
import pkg from "./package.json" with { type: "json" };

const root = join(import.meta.dir, "..", "examples");
const highlighter = `@pierre/highlights ${pkg.dependencies["@pierre/highlights"]}`;
// A theme with no colours: only the records are read.
const theme = { name: "records", appearance: "dark", style: {} };

function* snippets(dir: string): Generator<string> {
  for (const name of readdirSync(dir).sort()) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) yield* snippets(path);
    else if (name.endsWith(".ts") || name.endsWith(".go")) yield path;
  }
}

// bytes is the UTF-8 length of the first n UTF-16 units of s.
const bytes = (s: string, n: number) => Buffer.byteLength(s.slice(0, n));

for (const path of snippets(root)) {
  const code = readFileSync(path, "utf8");
  const lang = path.endsWith(".go") ? "go" : "ts";
  const t = new LiveTokenizer({ code, lang, theme } as never);
  t.flush();
  const rows: string[] = [];
  for (let line = 0; line < t.lineCount; line++) {
    const text = t.getLineText(line);
    const r = t.getLineRecords(line);
    let start = 0;
    for (let i = 0; i < r.data.length; i++) {
      const word = r.data[i]!;
      const end = r.format === "packed24" ? word & 0xffffff : word;
      const id = r.format === "packed24" ? word >>> 24 : r.data[++i]!;
      // A record carries the blanks after it; they belong to no token.
      const body = text.slice(start, end).trimEnd().length;
      if (body > 0) {
        const col = bytes(text, start) + 1;
        rows.push(JSON.stringify([line + 1, col, bytes(text, start + body) + 1, tokenNames[id]]));
      }
      start = end;
    }
  }
  t.dispose();
  const name = path.slice(path.lastIndexOf("/") + 1);
  const digest = createHash("sha256").update(code).digest("hex");
  const head = `{"source": ${JSON.stringify(name)}, "sha256": "${digest}", "highlighter": ${JSON.stringify(highlighter)}, "tokens": [`;
  writeFileSync(`${path}.tokens.json`, `${head}\n${rows.join(",\n")}\n]}\n`);
}

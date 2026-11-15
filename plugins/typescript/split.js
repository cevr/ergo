// The TypeScript Splitter (SEMWS WG4, level 1): one file cut into units
// by oxc-parser, the answer work's plugin protocol carries as UnitsAre.
//
// A unit is one top-level statement, keyed by the names it declares:
//   - an overload set (the declarations of one function name in a row)
//     is one unit;
//   - `export default` (and `export =`) is the key "default";
//   - an import is keyed by its module ("import ./x"), so two imports of
//     one module are one unit and its named specifiers are its members:
//     imports merge as a set;
//   - `declare module "m"`, `namespace N` and `declare global` are one
//     unit each, their statements its members;
//   - a statement that declares nothing is keyed by what it is: a call
//     by its callee and the string it is given first, so each test of a
//     test file is its own unit (`code test "adds"`), another statement
//     by its kind ("code IfStatement"); never by its offset nor by a
//     neighbour's name (a rename of the unit before it would move it).
// A key taken twice (interface merging, two calls of one callee with no
// string) keys the later ones "I #2": the one key that an edit of another
// unit (a rename of the first I) can move.
// Members are given for classes, interfaces, enums, object literals, a
// module's statements and an import's or export's specifiers.
//
// The texts join to the file: a unit runs from the line after the end of
// the statement before it (a comment on that statement's last line stays
// with it) to the line after its own end, so the comment above a
// statement belongs to its unit. The first unit starts at the file's
// first byte. So a line break added between a statement and the comment
// after it on its line moves that comment, which now has a line of its
// own, to the next unit: the one edit inside a unit (with "I #2" below)
// that changes another unit. A space, the edit work plugin-test makes,
// never does. Members' texts join to one run of their unit's text: a
// member runs from the end of the member before it (or the opening brace)
// to its own end, so its separator (a comma, a newline) leads it.

import { parseSync } from "oxc-parser";

const exts = [".ts", ".tsx", ".mts", ".cts"];

// lang is the dialect of path: tsx only for .tsx, so a cast `<T>x` in a
// .ts file parses and JSX in a .tsx file does.
export function lang(path) {
  return path.endsWith(".tsx") ? "tsx" : "ts";
}

export function isTypeScript(path) {
  return exts.some((e) => path.endsWith(e));
}

// units is the units of text, or { unreadable } when it does not parse.
export function units(file, text) {
  const r = parseSync(file, text, { lang: lang(file), sourceType: "module" });
  if (r.errors.length > 0) {
    const e = r.errors[0];
    return { unreadable: `${e.message}${e.labels?.[0] ? ` at ${e.labels[0].start}` : ""}` };
  }
  const commentAt = new Map(r.comments.map((c) => [c.start, c.end]));
  const groups = grouped(r.program.body);
  if (groups.length === 0) {
    return text === "" ? [] : [{ key: "", kind: "preamble", text, exported: false, members: [] }];
  }
  const out = [];
  const seen = new Map();
  let start = 0;
  for (let i = 0; i < groups.length; i++) {
    const g = groups[i];
    const end = i + 1 < groups.length ? cut(text, commentAt, g.end, groups[i + 1].start) : text.length;
    const unitText = text.slice(start, end);
    let { key, kind, exported } = describe(g.nodes[0], text);
    key = unique(seen, key === "" ? codeKey(g.nodes[0], text) : key);
    seen.set(key, true);
    out.push({ key, kind, text: unitText, exported, members: members(g.nodes[0], text, start, unitText) });
    start = end;
  }
  return out;
}

// codeKey is the key of a statement that declares nothing: "code", what
// it calls (its callee's text, when short) and the string it gives first
// (`test("adds", …)`, `describe("x", …)`), or its kind.
function codeKey(node, text) {
  let e = node.type === "ExpressionStatement" ? node.expression : null;
  if (e && e.type === "AwaitExpression") e = e.argument;
  if (!e || e.type !== "CallExpression") return `code ${node.type}`;
  const callee = text.slice(e.callee.start, e.callee.end).replace(/\s+/g, " ");
  const named = callee.length <= 60 ? callee : "call";
  const a = e.arguments[0];
  if (a && a.type === "Literal" && typeof a.value === "string") return `code ${named} ${JSON.stringify(a.value)}`;
  if (a && a.type === "TemplateLiteral" && a.expressions.length === 0) return `code ${named} ${JSON.stringify(a.quasis[0].value.cooked)}`;
  return `code ${named}`;
}

function unique(seen, key) {
  if (!seen.has(key)) return key;
  let n = 2;
  while (seen.has(`${key} #${n}`)) n++;
  return `${key} #${n}`;
}

// cut is where the unit after a statement that ends at end starts: after
// the first newline that follows end outside a comment, or at next (the
// next statement's start) when there is none before it.
function cut(text, commentAt, end, next) {
  let p = end;
  while (p < next) {
    const c = commentAt.get(p);
    if (c !== undefined) {
      p = c;
      continue;
    }
    if (text[p] === "\n") return p + 1;
    p++;
  }
  return next;
}

// inner is the declaration an export wraps, or the node.
function inner(node) {
  if (node.type === "ExportNamedDeclaration" && node.declaration) return node.declaration;
  return node;
}

function functionName(node) {
  const d = inner(node);
  if ((d.type === "FunctionDeclaration" || d.type === "TSDeclareFunction") && d.id) return d.id.name;
  return null;
}

// grouped is the statements, an overload set as one group.
function grouped(body) {
  const out = [];
  for (const s of body) {
    const name = functionName(s);
    const last = out[out.length - 1];
    if (name !== null && last && last.fn === name && last.open) {
      last.nodes.push(s);
      last.end = s.end;
      last.open = inner(s).type === "TSDeclareFunction";
      continue;
    }
    out.push({ nodes: [s], start: s.start, end: s.end, fn: name, open: name !== null && inner(s).type === "TSDeclareFunction" });
  }
  return out;
}

function names(pattern, out) {
  if (!pattern) return out;
  switch (pattern.type) {
    case "Identifier":
      out.push(pattern.name);
      break;
    case "ObjectPattern":
      for (const p of pattern.properties) names(p.type === "RestElement" ? p.argument : p.value, out);
      break;
    case "ArrayPattern":
      for (const e of pattern.elements) names(e && e.type === "RestElement" ? e.argument : e, out);
      break;
    case "AssignmentPattern":
      names(pattern.left, out);
      break;
    case "RestElement":
      names(pattern.argument, out);
      break;
  }
  return out;
}

function moduleName(d) {
  if (d.kind === "global" || d.global) return "global";
  if (d.id.type === "Literal") return `module ${d.id.value}`;
  let id = d.id;
  const parts = [];
  while (id && id.type === "TSQualifiedName") {
    parts.unshift(id.right.name);
    id = id.left;
  }
  if (id) parts.unshift(id.name);
  return `namespace ${parts.join(".")}`;
}

// describe is a statement's key ("" when it declares nothing), kind and
// whether it is exported.
function describe(node, text) {
  const exported = node.type.startsWith("Export") || node.type === "TSExportAssignment";
  switch (node.type) {
    case "ImportDeclaration":
      return { key: `import ${node.source.value}${node.importKind === "type" ? " type" : ""}`, kind: "import", exported };
    case "TSImportEqualsDeclaration":
      return { key: node.id.name, kind: "import", exported: node.isExport === true };
    case "ExportDefaultDeclaration":
    case "TSExportAssignment":
      return { key: "default", kind: "default", exported: true };
    case "ExportAllDeclaration":
      return { key: `export * from ${node.source.value}${node.exported ? ` as ${text.slice(node.exported.start, node.exported.end)}` : ""}`, kind: "export", exported };
    case "TSNamespaceExportDeclaration":
      return { key: `export as namespace ${node.id.name}`, kind: "export", exported };
    case "ExportNamedDeclaration":
      if (!node.declaration) return { key: `export${node.exportKind === "type" ? " type" : ""} from ${node.source ? node.source.value : ""}`, kind: "export", exported };
  }
  const d = inner(node);
  switch (d.type) {
    case "FunctionDeclaration":
    case "TSDeclareFunction":
      return { key: d.id ? d.id.name : "", kind: "function", exported };
    case "ClassDeclaration":
      return { key: d.id ? d.id.name : "", kind: "class", exported };
    case "TSInterfaceDeclaration":
      return { key: d.id.name, kind: "interface", exported };
    case "TSTypeAliasDeclaration":
      return { key: d.id.name, kind: "type", exported };
    case "TSEnumDeclaration":
      return { key: d.id.name, kind: "enum", exported };
    case "TSModuleDeclaration":
      return { key: moduleName(d), kind: "module", exported };
    case "VariableDeclaration": {
      const all = [];
      for (const v of d.declarations) names(v.id, all);
      return { key: all.join(","), kind: "variable", exported };
    }
  }
  return { key: "", kind: "code", exported };
}

function unwrap(e) {
  while (e && (e.type === "TSAsExpression" || e.type === "TSSatisfiesExpression" || e.type === "ParenthesizedExpression" || e.type === "TSTypeAssertion")) e = e.expression;
  return e;
}

// parts is the nodes that are a statement's members, with how each is
// keyed, and the offset their run starts after (the opening brace).
function parts(node, text) {
  const d = inner(node);
  const after = (n) => text.indexOf("{", n.start) + 1;
  switch (d.type) {
    case "ImportDeclaration": {
      const named = d.specifiers.filter((s) => s.type === "ImportSpecifier");
      if (named.length === 0) return null;
      return { open: text.lastIndexOf("{", named[0].start) + 1, nodes: named, key: (s) => text.slice(s.start, s.end).replace(/\s+/g, " ") };
    }
    case "ExportNamedDeclaration":
      if (d.specifiers.length === 0) return null;
      return { open: text.lastIndexOf("{", d.specifiers[0].start) + 1, nodes: d.specifiers, key: (s) => text.slice(s.start, s.end).replace(/\s+/g, " ") };
    case "ClassDeclaration":
      return { open: d.body.start + 1, nodes: d.body.body, key: (m) => memberKey(m, text) };
    case "TSInterfaceDeclaration":
      return { open: d.body.start + 1, nodes: d.body.body, key: (m) => memberKey(m, text) };
    case "TSEnumDeclaration":
      return { open: d.body.start + 1, nodes: d.body.members, key: (m) => (m.id.type === "Identifier" ? m.id.name : m.id.value) };
    case "TSModuleDeclaration": {
      let b = d.body;
      while (b && b.type === "TSModuleDeclaration") b = b.body;
      if (!b || b.type !== "TSModuleBlock") return null;
      return { open: b.start + 1, nodes: b.body, key: (s) => describe(s, text).key };
    }
    case "VariableDeclaration": {
      if (d.declarations.length !== 1) return null;
      const o = unwrap(d.declarations[0].init);
      if (!o || o.type !== "ObjectExpression") return null;
      return { open: o.start + 1, nodes: o.properties, key: (p) => memberKey(p, text) };
    }
    case "ExportDefaultDeclaration": {
      const o = unwrap(d.declaration);
      if (o && o.type === "ObjectExpression") return { open: o.start + 1, nodes: o.properties, key: (p) => memberKey(p, text) };
      if (o && (o.type === "ClassDeclaration" || o.type === "ClassExpression")) return { open: o.body.start + 1, nodes: o.body.body, key: (m) => memberKey(m, text) };
      return null;
    }
  }
  return null;
}

// memberKey is a member's key: its name, with static, get or set before
// it; a member with no name (a call signature, a spread) by its text.
function memberKey(m, text) {
  const k = m.key;
  let name;
  if (!k) name = text.slice(m.start, m.end).replace(/\s+/g, " ");
  else if (m.computed) name = `[${text.slice(k.start, k.end)}]`;
  else if (k.type === "Identifier" || k.type === "PrivateIdentifier") name = (k.type === "PrivateIdentifier" ? "#" : "") + k.name;
  else if (k.type === "Literal") name = String(k.value);
  else name = text.slice(k.start, k.end);
  const pre = [];
  if (m.static) pre.push("static");
  if (m.kind === "get" || m.kind === "set") pre.push(m.kind);
  return [...pre, name].join(" ");
}

// members is a statement's members: a member runs from the end of the one
// before it (the opening brace for the first) to its own end; a member of
// one key after another of that key (an overload) joins it.
function members(node, text, unitStart, unitText) {
  const p = parts(node, text);
  if (!p || p.nodes.length === 0) return [];
  const out = [];
  const seen = new Map();
  let start = p.open;
  let lastKey = null;
  for (const m of p.nodes) {
    let key = p.key(m);
    if (key === "") key = "code";
    if (key === lastKey) {
      const last = out[out.length - 1];
      last.text += text.slice(start, m.end);
      start = m.end;
      continue;
    }
    lastKey = key;
    const k = unique(seen, key);
    seen.set(k, true);
    out.push({ key: k, kind: "member", text: text.slice(start, m.end), exported: false, members: [] });
    start = m.end;
  }
  // The run must lie inside the unit (a member that starts before it is
  // the parser's, not ours, to explain): else no members.
  const runStart = p.open - unitStart;
  const run = out.map((u) => u.text).join("");
  if (runStart < 0 || unitText.slice(runStart, runStart + run.length) !== run) return [];
  return out;
}

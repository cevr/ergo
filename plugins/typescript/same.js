#!/usr/bin/env bun
// same.js A B: exit 0 when the two TypeScript files hold the same units
// (each unit's text, in any order), 1 when they do not, 2 when one does
// not parse. scripts/mergereplay uses it to tell a merge that is the
// committed file in another order of its declarations from one that is
// not.

import { readFileSync } from "node:fs";
import { units } from "./split.js";

function texts(path) {
  const us = units(path, readFileSync(path, "utf8"));
  if (us.unreadable !== undefined) process.exit(2);
  return us.map((u) => u.text.trim()).sort();
}

const [a, b] = process.argv.slice(2);
const x = texts(a);
const y = texts(b);
process.exit(x.length === y.length && x.every((t, i) => t === y[i]) ? 0 : 1);

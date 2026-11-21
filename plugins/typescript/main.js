#!/usr/bin/env bun
// work's TypeScript plugin (SEMWS WG4): reads work's requests on standard
// input, one JSON object a line, and writes one answer a line, in order
// (modules/work/work/plugin.ergo). Level 1: it answers UnitsOf; every
// other request is NotProvided, never an empty answer.

import { readFileSync } from "node:fs";
import { units } from "./split.js";

export function answer(request) {
  if (request._tag !== "UnitsOf") return { _tag: "NotProvided", op: request._tag.replace(/Of$/, "") };
  const u = units(request.file, request.text);
  if (u.unreadable !== undefined) return { _tag: "Unreadable", file: request.file, why: u.unreadable };
  return { _tag: "UnitsAre", units: u };
}

export function serve(input) {
  const out = [];
  for (const line of input.split("\n")) {
    if (line.trim() === "") continue;
    let request;
    try {
      request = JSON.parse(line);
    } catch {
      out.push(JSON.stringify({ _tag: "Unanswered", command: "", why: "a line that is no Request" }));
      continue;
    }
    out.push(JSON.stringify(answer(request)));
  }
  return out.map((l) => l + "\n").join("");
}

if (import.meta.main) process.stdout.write(serve(readFileSync(0, "utf8")));

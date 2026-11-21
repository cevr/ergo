"use strict";
// AS8's acceptance check of @cvr/tn1-lint's JS wrapper (tools/npm/tn1-lint/lint.js),
// counted, never timed:
//
//   node scripts/npm-sidecar-check.js BINARY N
//
// BINARY is a tn1-lint built with `serve`. Over N copies of the TS1
// conformance vectors' TypeScript source, linted with each of the vectors'
// networks, it checks that
//   1. the wrapper starts one process, where one `tn1-lint lint` per file
//      and network (the spawnSync path) starts N x networks;
//   2. the two paths' findings are equal, file by file;
//   3. no `BINARY serve` process is left once the run's process has ended:
//      the run is a child process whose standard error the sidecar shares,
//      so the end of that pipe is the event that every holder (the run and
//      its sidecar) has exited; then /proc is read for a survivor.
// It prints one line per fact and exits 1 when one fails.
const { spawn, spawnSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const root = path.join(__dirname, "..");
const vectors = path.join(root, "modules/tn1/testdata/vectors");

function fail(message) {
  console.log(`FAIL ${message}`);
  process.exitCode = 1;
}

/** The networks, and N copies of the vectors' TypeScript source. */
function inputs(count) {
  const nets = fs
    .readdirSync(path.join(vectors, "nets"))
    .filter((name) => name.endsWith(".tn1"))
    .sort()
    .map((name) => path.join(vectors, "nets", name));
  const sources = fs.readdirSync(path.join(vectors, "src")).filter((name) => name.endsWith(".ts.txt"));
  const texts = sources.map((name) => fs.readFileSync(path.join(vectors, "src", name), "utf8"));
  const files = [];
  for (let i = 0; i < count; i++) {
    const text = texts[i % texts.length];
    files.push({ path: `fixture-${i}.ts`, text });
  }
  return { nets, files };
}

/** The run: lint every file with every net through `mode`, and print what it saw as JSON. */
function run(binary, count, mode) {
  const { nets, files } = inputs(count);
  const findings = [];
  let starts = 0;
  if (mode === "spawn") {
    for (const file of files) {
      for (const net of nets) {
        const result = spawnSync(binary, ["lint", "--rule", "vector", net, "-"], { input: file.text, encoding: "utf8" });
        starts += 1;
        if (result.status !== 0) throw new Error(`tn1-lint lint: ${result.stderr}`);
        findings.push(JSON.parse(result.stdout).diagnostics.map((d) => ({ ...d, filename: file.path })));
      }
    }
  } else if (mode === "broken") {
    // A sidecar that cannot start or keeps exiting: every call fails, none
    // waits, and the starts stay bounded.
    process.env.TN1_LINT_BINARY = binary;
    const { lintSync, processStarts } = require(path.join(root, "tools/npm/tn1-lint/lint.js"));
    for (let i = 0; i < count; i++) {
      try {
        lintSync({ rule: "vector", weights: nets[0], path: files[i].path, text: files[i].text });
        findings.push("answered");
      } catch (error) {
        findings.push(`${error.code ?? "-"} ${error.message}`);
      }
    }
    starts = processStarts();
  } else {
    process.env.TN1_LINT_BINARY = binary;
    const { lintSync, processStarts } = require(path.join(root, "tools/npm/tn1-lint/lint.js"));
    for (const file of files) {
      for (const net of nets) {
        findings.push(lintSync({ rule: "vector", weights: net, path: file.path, text: file.text }));
      }
    }
    starts = processStarts();
  }
  process.stdout.write(JSON.stringify({ starts, findings }));
}

/** Pids of `binary serve` processes still running. */
function survivors(binary) {
  const found = [];
  for (const entry of fs.readdirSync("/proc")) {
    if (!/^\d+$/.test(entry)) continue;
    let argv;
    try {
      argv = fs.readFileSync(`/proc/${entry}/cmdline`, "utf8").split("\0");
    } catch {
      continue;
    }
    if (argv[0] === binary && argv[1] === "serve") found.push(Number(entry));
  }
  return found;
}

/** Run `mode` in a child; resolve with its output once the child and every holder of its stderr are gone. */
function child(binary, count, mode) {
  return new Promise((resolve, reject) => {
    const proc = spawn(process.execPath, [__filename, "--run", binary, String(count), mode], {
      stdio: ["ignore", "pipe", "pipe"],
    });
    let out = "";
    let err = "";
    let exitCode;
    let ended = false;
    const done = () => {
      if (exitCode === undefined || !ended) return;
      if (exitCode !== 0) reject(new Error(`${mode} run exited ${exitCode}: ${err}`));
      else resolve(JSON.parse(out));
    };
    proc.stdout.on("data", (chunk) => (out += chunk));
    proc.stderr.on("data", (chunk) => (err += chunk));
    proc.stderr.on("end", () => {
      ended = true;
      done();
    });
    proc.on("exit", (code) => {
      exitCode = code;
      done();
    });
  });
}

async function main() {
  const [binaryArg, countArg] = process.argv.slice(2);
  if (!binaryArg || !countArg) {
    console.error("usage: node scripts/npm-sidecar-check.js BINARY N");
    process.exit(2);
  }
  const binary = path.resolve(binaryArg);
  const count = Number(countArg);
  const spawned = await child(binary, count, "spawn");
  const sidecar = await child(binary, count, "sidecar");
  const nets = inputs(count).nets.length;
  console.log(`files ${count}, networks ${nets}`);
  console.log(`process starts: sidecar ${sidecar.starts}, spawnSync ${spawned.starts}`);
  if (sidecar.starts !== 1) fail(`the sidecar path started ${sidecar.starts} processes, not 1`);
  if (spawned.starts !== count * nets) fail(`the spawnSync path started ${spawned.starts}, not ${count * nets}`);
  const a = JSON.stringify(sidecar.findings);
  const b = JSON.stringify(spawned.findings);
  const total = sidecar.findings.reduce((sum, list) => sum + list.length, 0);
  console.log(`findings: ${total} over ${sidecar.findings.length} file-network pairs, equal: ${a === b}`);
  if (a !== b) fail("the two paths' findings differ");
  const left = survivors(binary);
  console.log(`serve processes left after the run: ${left.length}`);
  if (left.length > 0) fail(`serve processes left: ${left.join(" ")}`);
  await broken();
}

// A start that throws (/dev/null/tn1-lint: exec fails with ENOTDIR, which
// spawn throws as it throws ENOMEM when fork fails), a start that fails
// later (ENOENT, an error event) and a sidecar that exits at once: each
// call fails with the reason, the first failed start ends the starts, and
// an exiting sidecar is started at most 3 times.
async function broken() {
  const cases = [
    { binary: "/dev/null/tn1-lint", reason: "cannot start /dev/null/tn1-lint: spawn ENOTDIR", starts: 0 },
    { binary: "/nonexistent/tn1-lint", reason: "cannot start /nonexistent/tn1-lint: spawn /nonexistent/tn1-lint ENOENT", starts: 1 },
    { binary: "/bin/false", reason: "not starting it again", starts: 3 },
  ];
  for (const c of cases) {
    const got = await child(c.binary, 5, "broken");
    const last = got.findings[got.findings.length - 1];
    // Once the sidecar is down, a call says so by its code (the last call is after that).
    const ok =
      got.starts === c.starts &&
      got.findings.every((f) => f !== "answered") &&
      last.startsWith("TN1_SIDECAR_DOWN ") &&
      last.includes(c.reason);
    console.log(`${c.binary}: starts ${got.starts}, last reason: ${last}`);
    if (!ok) fail(`${c.binary}: want ${c.starts} starts and every call failed, the last with code TN1_SIDECAR_DOWN and "${c.reason}"`);
    if (c.starts === 0 && !got.findings.every((f) => f === got.findings[0]))
      fail(`${c.binary}: a failed start is tried again: ${got.findings.join(" | ")}`);
  }
}

if (process.argv[2] === "--run") {
  run(path.resolve(process.argv[3]), Number(process.argv[4]), process.argv[5]);
} else {
  main().catch((error) => {
    console.log(`FAIL ${error.message}`);
    process.exit(1);
  });
}

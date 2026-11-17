"use strict";
// The worker thread of lint.js: it keeps one `tn1-lint serve` process,
// writes each request it is posted as a line, and posts back each reply
// line, then wakes the caller blocked in Atomics.wait. A sidecar that
// exits fails the request in flight; the next request starts another.
const { spawn } = require("node:child_process");
const readline = require("node:readline");
const { workerData } = require("node:worker_threads");

const READY = 0;
const STARTS = 1;
const GROUP = 2;

const { binary, shared, port } = workerData;
let child;
let pending = false;

function reply(message) {
  pending = false;
  port.postMessage(message);
  Atomics.store(shared, READY, 1);
  Atomics.notify(shared, READY);
}

function launch() {
  // detached: the sidecar leads its own process group, which lint.js's
  // exit hook signals; its standard input closing ends it first.
  const started = spawn(binary, ["serve"], { stdio: ["pipe", "pipe", "inherit"], detached: true });
  Atomics.add(shared, STARTS, 1);
  Atomics.store(shared, GROUP, started.pid ?? 0);
  readline.createInterface({ input: started.stdout }).on("line", (line) => {
    try {
      reply(JSON.parse(line));
    } catch {
      reply({ diagnostics: [], error: `tn1-lint: an unreadable reply: ${line.slice(0, 200)}` });
    }
  });
  const gone = (reason) => {
    if (child !== started) return;
    child = undefined;
    Atomics.store(shared, GROUP, 0);
    if (pending) reply({ diagnostics: [], error: reason });
  };
  started.on("error", (error) => gone(`tn1-lint: cannot start ${binary}: ${error.message}`));
  started.on("exit", (code, signal) =>
    gone(`tn1-lint: the sidecar exited (${signal ?? `status ${code}`})`),
  );
  started.stdin.on("error", () => {});
  child = started;
}

port.on("message", (request) => {
  if (child === undefined) launch();
  pending = true;
  child.stdin.write(`${JSON.stringify(request)}\n`);
});

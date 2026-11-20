"use strict";
// The worker thread of lint.js: it keeps one `tn1-lint serve` process,
// writes each request line it is posted, and posts back each reply line,
// decoded against Reply (wire.js; one that is not the declared JSON is an
// error reply), then wakes the caller blocked in Atomics.wait. A sidecar that
// exits fails the request in flight; the next request starts another, up
// to MAX_STARTS per process. A start that fails (spawn throws, as fork
// does with ENOMEM in a process holding much memory, or the binary cannot
// run) is replied at once with `failed: true`, and lint.js starts no more.
const { spawn } = require("node:child_process");
const readline = require("node:readline");
const { workerData } = require("node:worker_threads");
const { decodeReply } = require("./wire.js");

const READY = 0;
const STARTS = 1;
const GROUP = 2;

// The most sidecar processes one process starts: a sidecar that keeps
// exiting is not started once per file.
const MAX_STARTS = 3;

const { binary, shared, port } = workerData;
let child;
let pending = false;

function reply(message) {
  pending = false;
  port.postMessage(message);
  Atomics.store(shared, READY, 1);
  Atomics.notify(shared, READY);
}

/** A start that failed: this reply, and every later one, is this reason. */
function failed(reason) {
  reply({ diagnostics: [], error: reason, failed: true });
}

function launch() {
  // detached: the sidecar leads its own process group, which lint.js's
  // exit hook signals; its standard input closing ends it first.
  const started = spawn(binary, ["serve"], { stdio: ["pipe", "pipe", "inherit"], detached: true });
  Atomics.add(shared, STARTS, 1);
  Atomics.store(shared, GROUP, started.pid ?? 0);
  readline.createInterface({ input: started.stdout }).on("line", (line) => {
    let decoded;
    try {
      decoded = decodeReply(line);
    } catch (error) {
      decoded = { diagnostics: [], error: error.message };
    }
    reply(decoded);
  });
  const gone = (reason, fatal) => {
    if (child !== started) return;
    child = undefined;
    Atomics.store(shared, GROUP, 0);
    if (!pending) return;
    if (fatal) failed(reason);
    else reply({ diagnostics: [], error: reason });
  };
  // An error event is a start that failed (ENOENT, EACCES): no later
  // request can do better.
  started.on("error", (error) => gone(`tn1-lint: cannot start ${binary}: ${error.message}`, true));
  started.on("exit", (code, signal) =>
    gone(`tn1-lint: the sidecar exited (${signal ?? `status ${code}`})`, false),
  );
  started.stdin.on("error", () => {});
  child = started;
}

port.on("message", (request) => {
  // request is a line of wire.js's encodeRequest.
  pending = true;
  if (child === undefined) {
    if (Atomics.load(shared, STARTS) >= MAX_STARTS) {
      failed(`tn1-lint: the sidecar exited ${MAX_STARTS} times; not starting it again`);
      return;
    }
    try {
      launch();
    } catch (error) {
      failed(`tn1-lint: cannot start ${binary}: ${error.message}`);
      return;
    }
  }
  child.stdin.write(`${request}\n`);
});

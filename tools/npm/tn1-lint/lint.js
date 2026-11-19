"use strict";
// The synchronous API of @cvr/tn1-lint, for a caller that cannot wait on
// a promise (an oxlint rule): lintSync(request) is the diagnostics
// `tn1-lint lint` reports for one text.
//
// One `tn1-lint serve` process answers every call of a process: a worker
// thread starts it on the first call and keeps it, and a call blocks on
// Atomics.wait until the worker has its reply (the pattern of synckit and
// oxlint-plugin-jev). N files cost one process start, not N.
//
//   const { lintSync, processStarts } = require("@cvr/tn1-lint/lint");
//   lintSync({ rule, weights, path, text }) // => [{ message, code, severity, filename, labels }]
//
// The request and reply are tn1-lint's Ergo types Request and Reply
// (wire.js, generated from them by examples/tn1-lint/ts/wire.ts); a reply
// that is not the declared JSON throws, as a failure does.
//
// A failure (unreadable weights, a malformed network, a sidecar that
// died) throws an Error with tn1-lint's reason. A sidecar that cannot
// start (fork failing with ENOMEM in a process that holds much memory, a
// binary that cannot run), or a worker that stopped answering, fails that
// call and every later one at once with the same reason: no further start
// is tried and no call waits again. Such an Error has code
// "TN1_SIDECAR_DOWN", so a caller (the oxlint rule) can fail its whole run
// once instead of reporting the same failure on every file. The process the worker
// starts is in its own process group; it ends when this process's pipe
// to it closes, and the exit hook below signals that group, the one it
// started, in case it has not.
const path = require("node:path");
const { MessageChannel, Worker, receiveMessageOnPort } = require("node:worker_threads");
const { encodeRequest } = require("./wire.js");

// Slots of the shared Int32Array: the reply flag, the process-start
// count, and the process group the worker started (0 when none).
const READY = 0;
const STARTS = 1;
const GROUP = 2;

// A safety net for a worker that died without replying, never a time
// limit on a reply: one file's lint takes milliseconds.
const HANG_MS = 120000;

let sidecar;

/** The binary: TN1_LINT_BINARY, or this package's bin. */
function binary() {
  return process.env.TN1_LINT_BINARY || path.join(__dirname, "bin", "tn1-lint");
}

function start() {
  const shared = new Int32Array(new SharedArrayBuffer(3 * Int32Array.BYTES_PER_ELEMENT));
  const { port1, port2 } = new MessageChannel();
  const worker = new Worker(path.join(__dirname, "worker.js"), {
    workerData: { binary: binary(), shared, port: port2 },
    transferList: [port2],
  });
  worker.unref();
  port1.unref();
  sidecar = { worker, port: port1, shared, broken: undefined };
  process.once("exit", stop);
}

/** Signal the process group the worker started, if it is still there. */
function stop() {
  if (sidecar === undefined) return;
  const group = Atomics.load(sidecar.shared, GROUP);
  if (group > 0) {
    try {
      process.kill(-group, "SIGTERM");
    } catch {
      // Gone already: it ended when its standard input closed.
    }
  }
}

/** The Error of a sidecar that is down for this process: no later call can succeed. */
function down(reason) {
  const error = new Error(reason);
  error.code = "TN1_SIDECAR_DOWN";
  return error;
}

/** Diagnostics for request = { rule, weights, path, text }. */
function lintSync(request) {
  if (sidecar === undefined) start();
  const { port, shared } = sidecar;
  if (sidecar.broken !== undefined) throw down(sidecar.broken);
  Atomics.store(shared, READY, 0);
  port.postMessage(encodeRequest(request));
  if (Atomics.wait(shared, READY, 0, HANG_MS) === "timed-out") {
    sidecar.broken = "tn1-lint: the sidecar did not answer (its worker stopped)";
    throw down(sidecar.broken);
  }
  const received = receiveMessageOnPort(port);
  if (received === undefined) throw new Error("tn1-lint: the sidecar's reply was lost");
  const reply = received.message;
  if (reply.failed) {
    sidecar.broken = reply.error;
    throw down(reply.error);
  }
  if (reply.error) throw new Error(reply.error);
  return reply.diagnostics;
}

/** How many tn1-lint processes this process has started. */
function processStarts() {
  if (sidecar === undefined) return 0;
  return Atomics.load(sidecar.shared, STARTS);
}

/** The process group of the running sidecar, or 0. */
function processGroup() {
  if (sidecar === undefined) return 0;
  return Atomics.load(sidecar.shared, GROUP);
}

module.exports = { lintSync, processStarts, processGroup };

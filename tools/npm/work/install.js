// postinstall of @cevr/work: put the platform package's native binary at
// bin/work, in place of the shell fallback, so the bin npm links runs the
// binary itself (esbuild's pattern). A failure keeps the fallback, which
// finds the same binary at each run.
"use strict";
const fs = require("fs");
const path = require("path");

const platforms = {
  "linux-x64": "@cevr/work-linux-x64",
  "linux-arm64": "@cevr/work-linux-arm64",
  "darwin-arm64": "@cevr/work-darwin-arm64",
};

function main() {
  const pkg = platforms[`${process.platform}-${process.arch}`];
  if (!pkg) {
    console.error(`work: no prebuilt binary for ${process.platform} ${process.arch}`);
    return;
  }
  let native;
  try {
    native = require.resolve(`${pkg}/bin/work`);
  } catch {
    console.error(`work: ${pkg} is not installed; bin/work stays the shell fallback`);
    return;
  }
  const bin = path.join(__dirname, "bin", "work");
  const next = `${bin}.${process.pid}`;
  try {
    // A hard link where the two packages share a file system, else a copy;
    // then one rename, so bin/work is never half written.
    try {
      fs.linkSync(native, next);
    } catch {
      fs.copyFileSync(native, next);
    }
    fs.chmodSync(next, 0o755);
    fs.renameSync(next, bin);
  } catch (e) {
    try {
      fs.unlinkSync(next);
    } catch {}
    console.error(`work: could not place the native binary (${e.message}); bin/work stays the shell fallback`);
  }
}

main();

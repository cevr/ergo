// postinstall of a wrapper package of tools/npm (scripts/npm.sh copies it
// beside the wrapper's package.json): put the platform package's native
// binary at bin/NAME, in place of the shell launcher, so the bin npm links
// runs the binary itself (esbuild's pattern). NAME is the package's one
// bin; the platform packages are its optional dependencies PACKAGE-OS-CPU.
// A failure keeps the launcher, which finds the same binary at each run.
"use strict";
const fs = require("fs");
const path = require("path");
const manifest = require("./package.json");

function main() {
  const name = Object.keys(manifest.bin)[0];
  const pkg = `${manifest.name}-${process.platform}-${process.arch}`;
  if (!(manifest.optionalDependencies || {})[pkg]) {
    console.error(`${name}: no prebuilt binary for ${process.platform} ${process.arch}`);
    return;
  }
  let native;
  try {
    native = require.resolve(`${pkg}/bin/${name}`);
  } catch {
    console.error(`${name}: ${pkg} is not installed; bin/${name} stays the shell launcher`);
    return;
  }
  const bin = path.join(__dirname, "bin", name);
  const next = `${bin}.${process.pid}`;
  try {
    // A hard link where the two packages share a file system, else a copy;
    // then one rename, so bin/NAME is never half written.
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
    console.error(`${name}: could not place the native binary (${e.message}); bin/${name} stays the shell launcher`);
  }
}

main();

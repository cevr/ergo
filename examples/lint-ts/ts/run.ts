// run.ts runs the built lint-ts (ERGO_BIN) with this script's arguments
// and passes on its output and exit status, so a case of this ts-tagged
// program can run the binary itself.
const bin = process.env.ERGO_BIN
if (!bin) throw new Error("ERGO_BIN names the built lint-ts")
const run = Bun.spawnSync([bin, ...process.argv.slice(2)])
process.stdout.write(run.stdout)
process.stderr.write(run.stderr)
process.exit(run.exitCode ?? 1)

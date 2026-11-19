// measure.ts: instructions and heap bytes per event of plusthree's scenarios,
// for the Ergo program (ergo/cmd/p3bench) and the hand-written Go control
// (testdata/control). See README.md.
//
//   bun measure.ts <p3bench binary> <control binary> <out.json> [N]
//
// Instructions per event: (instructions:u at N - at 0) / N, by perf stat,
// in user mode (the workbox's perf_event_paranoid is 2). Heap bytes per
// event: (peak resident KiB at N - at 0) * 1024 / N with GOGC=off, so every
// allocation stays resident; a run that allocates nothing per event shows
// no growth beyond the page noise. Each count is taken twice.
import { $ } from "bun";

const [ergoBin, controlBin, outPath, nArg] = process.argv.slice(2);
const N = Number(nArg ?? 4 * 1048576);
const perf = process.env.PERF ?? "/home/exedev/.local/bin/perf";
const env = { ...process.env, GOMAXPROCS: "1" };

const rows: [string, string[], string[]][] = [
  ["S1", [ergoBin, "s1", "fold"], [controlBin, "s1"]],
  ["S2", [ergoBin, "s2", "fold"], [controlBin, "s2"]],
  ["S3", [ergoBin, "s3", "fold"], [controlBin, "s3"]],
  ["S4", [ergoBin, "s4", "fold"], [controlBin, "s4"]],
  ["S1 step", [ergoBin, "s1", "step"], [controlBin, "s1"]],
  ["S2 step", [ergoBin, "s2", "step"], [controlBin, "s2"]],
  ["S3 step", [ergoBin, "s3", "step"], [controlBin, "s3"]],
  ["S4 step", [ergoBin, "s4", "step"], [controlBin, "s4"]],
  ["S7", [ergoBin, "s7", "timed"], [controlBin, "s7"]],
  ["S1 actor", [ergoBin, "s1", "actor"], [controlBin, "s1actor"]],
  ["S7 actor", [ergoBin, "s7", "actor"], [controlBin, "s7actor"]],
];
// The live runner costs microseconds an event: a smaller N keeps its runs short.
const nOf = (name: string) => (name.endsWith("actor") ? N / 16 : N);

async function instructions(cmd: string[], n: number): Promise<number> {
  const r = await $`${perf} stat -x , -e instructions:u ${cmd} ${String(n)}`.env(env).nothrow().quiet();
  if (r.exitCode !== 0) throw new Error(`${cmd.join(" ")} ${n}: exit ${r.exitCode}: ${r.stderr}`);
  const line = r.stderr.toString().split("\n").find((l) => l.includes("instructions"));
  if (!line) throw new Error("no instructions line: " + r.stderr);
  return Number(line.split(",")[0]);
}

async function peakKiB(cmd: string[], n: number): Promise<number> {
  const r = await $`/usr/bin/time -f %M ${cmd} ${String(n)}`.env({ ...env, GOGC: "off" }).nothrow().quiet();
  if (r.exitCode !== 0) throw new Error(`${cmd.join(" ")} ${n}: exit ${r.exitCode}: ${r.stderr}`);
  const lines = r.stderr.toString().trim().split("\n");
  return Number(lines[lines.length - 1]);
}

async function perEvent(cmd: string[], n: number) {
  const ir: number[] = [];
  for (let k = 0; k < 2; k++) ir.push(((await instructions(cmd, n)) - (await instructions(cmd, 0))) / n);
  const bytes: number[] = [];
  for (let k = 0; k < 2; k++) bytes.push((((await peakKiB(cmd, n)) - (await peakKiB(cmd, 0))) * 1024) / n);
  return { n, ir, bytes };
}

const out: Record<string, unknown> = { N, measured: new Date().toISOString(), compiler: process.env.ERGO_COMMIT ?? "unknown" };
for (const [name, ergo, control] of rows) {
  const e = await perEvent(ergo, nOf(name));
  const g = await perEvent(control, nOf(name));
  out[name] = { ergo: e, control: g };
  console.log(name.padEnd(8), "ergo", e.ir.map((x) => x.toFixed(2)).join("/"), "instr", e.bytes.map((x) => x.toFixed(3)).join("/"), "B |",
    "go", g.ir.map((x) => x.toFixed(2)).join("/"), "instr", g.bytes.map((x) => x.toFixed(3)).join("/"), "B");
}
await Bun.write(outPath, JSON.stringify(out, null, 1));

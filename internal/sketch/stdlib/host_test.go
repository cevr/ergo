package stdlib_test

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdHostLawsRun runs the check-time laws of std host in process and holds
// every host row of runners.txt to a pass.
func TestStdHostLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("os/host")
	if err != nil {
		t.Fatalf("import os/host: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 200_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate host: %v", err)
	}
	results := map[string][]eval.Result{}
	for _, r := range rep.Results {
		name := strings.TrimPrefix(r.Name, "law ")
		if head, _, ok := strings.Cut(name, " for "); ok {
			name = head
		}
		if head, _, ok := strings.Cut(name, " under "); ok {
			name = head
		}
		key := "os/host " + name
		results[key] = append(results[key], r)
	}
	laws := map[string]lawledger.Law{}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		rs := results[row.Key()]
		statement, _ := lawledger.StdStatement(row.Key())
		laws[row.Key()] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			if len(rs) == 0 {
				t.Errorf("std %s has no law %s at check time", row.Pkg, row.Law)
				return
			}
			passed := 0
			for _, r := range rs {
				switch r.Status {
				case eval.Passed:
					if r.Evidence != "tested" && r.Evidence != "proved" {
						t.Errorf("law %s (%s): passed with evidence=%q", row.Key(), r.Name, r.Evidence)
					} else {
						passed++
					}
				case eval.Skipped, eval.Native:
				default:
					t.Errorf("law %s (%s): %s (%s) after %d cases, seed %d, case %d: %+v", row.Key(), r.Name, r.Status, r.Reason, r.Cases, r.Seed, r.Case, r.Arguments)
				}
			}
			if passed == 0 {
				t.Errorf("law %s: no run passed (%+v)", row.Key(), rs)
			}
		}}
	}
	lawledger.Run(t, laws)
}

const hostOracleSrc = `import hex
import io
import option
import os
import os/host
import strconv
import strings

type customVolProv { mount host.Mount } implements host.Volumes

func Mounts(c customVolProv) []host.Mount fails host.Unavailable | host.Malformed {
	[]host.Mount{c.mount}
}

func MountForPath(c customVolProv, path string) host.Mount fails host.Unavailable | host.NotFound | host.Malformed {
	host.ResolveMount([]host.Mount{c.mount}, path)
}

func formatMountKind(k host.MountKind) string {
	match k {
	case host.PrimaryMount:
		"primary"
	case host.BindMount:
		"bind"
	case host.SubvolumeMount:
		"subvol"
	}
}

func formatMount(m host.Mount) string {
	let volPart = match m.Volume {
	case option.None:
		"NONE"
	case option.Some[host.Volume] as v:
		"\(v.Value.TotalBytes):\(v.Value.FreeBytes):\(v.Value.AvailableBytes):\(v.Value.UsedBytes)"
	}
	"\(m.Info.MountID)|\(m.Info.ParentID)|\(hex.Encode(m.Info.DeviceID))|\(hex.Encode(m.Info.Root))|\(hex.Encode(m.Info.MountPoint))|\(hex.Encode(m.Info.FSType))|\(hex.Encode(m.Info.Source))|\(formatMountKind(m.Info.Kind))|\(volPart)"
}

func formatLoad(l host.LoadAvg) string {
	"\(strconv.FormatFloat(l.One, 'f', 4))|\(strconv.FormatFloat(l.Five, 'f', 4))|\(strconv.FormatFloat(l.Fifteen, 'f', 4))|\(l.Running)|\(l.Entities)"
}

func formatMem(m host.Memory) string {
	"\(m.TotalBytes)|\(m.AvailableBytes)|\(m.FreeBytes)|\(m.UsedBytes)"
}

func formatVol(v host.Volume) string {
	"\(hex.Encode(v.Path))|\(v.TotalBytes)|\(v.FreeBytes)|\(v.AvailableBytes)|\(v.UsedBytes)"
}

func checkAllProcessesValid(ps []host.Process) bool {
	let mut prev = 0
	for p in ps {
		if p.PID <= 0 || p.PID <= prev || p.PPID < 0 || p.State.Len() != 1 || p.Threads < 0 || p.UserTicks < int64(0) || p.SysTicks < int64(0) || p.StartTicks < int64(0) || p.RSSBytes < int64(0) || p.VSizeBytes < int64(0) {
			return false
		}
		prev = p.PID
	}
	true
}

func verifyPureNative() string {
	let la = host.ParseLoadAvg("2.50 1.25 0.50 3/120 9999\n") catch {
	case host.Malformed as err:
		return "ERR loadavg: " + err.Reason
	}
	if la.One != 2.50 || la.Five != 1.25 || la.Fifteen != 0.50 || la.Running != 3 || la.Entities != 120 {
		return "ERR loadavg values"
	}
	let mem = host.ParseMeminfo("MemTotal: 1000 kB\nMemFree: 250 kB\nMemAvailable: 600 kB\n") catch {
	case host.Malformed as err:
		return "ERR meminfo: " + err.Reason
	}
	if mem.TotalBytes != int64(1024000) || mem.FreeBytes != int64(256000) || mem.AvailableBytes != int64(614400) || mem.UsedBytes != int64(409600) {
		return "ERR meminfo values"
	}
	let st = host.ParseProcStat("42 (my (proc) name) S 7 1 1 0 -1 0 0 0 0 0 11 22 0 0 20 0 3 0 88 65536 4 0 0\n") catch {
	case host.Malformed as err:
		return "ERR stat: " + err.Reason
	}
	if st.PID != 42 || st.Comm != "my (proc) name" || st.State != "S" || st.PPID != 7 || st.UserTicks != int64(11) || st.SysTicks != int64(22) || st.Threads != 3 || st.StartTicks != int64(88) || st.VSizeBytes != int64(65536) || st.RSSPages != int64(4) {
		return "ERR stat values"
	}
	let stm = host.ParseProcStatm("16 5 2 0 0 0 0\n", int64(4096)) catch {
	case host.Malformed as err:
		return "ERR statm: " + err.Reason
	}
	if stm.SizeBytes != int64(65536) || stm.ResidentBytes != int64(20480) || stm.SharedBytes != int64(8192) {
		return "ERR statm values"
	}
	let args = host.ParseCmdline("prog\x00\x00--flag\x00\x00")
	if args.Len() != 3 || args[0] != "prog" || args[1] != "" || args[2] != "--flag" {
		return "ERR cmdline values"
	}
	let pct = host.CPUPercent(int64(250), 2.0, int64(100))
	if pct != 125.0 || host.CPUPercent(int64(0), 2.0, int64(100)) != 0.0 {
		return "ERR cpu percent"
	}
	let staticHost = host.NewStatic(
		host.Status{
			Cores:   4,
			Load:    la,
			Memory:  mem,
			Volumes: []host.Volume{host.Volume{Path: "/", TotalBytes: int64(1000), FreeBytes: int64(400), AvailableBytes: int64(300), UsedBytes: int64(600)}},
		},
		[]host.Process{host.Process{PID: 10, PPID: 1, State: "S", Comm: "worker", Cmdline: []string{"worker"}, UserTicks: int64(3), SysTicks: int64(4), StartTicks: int64(5), RSSBytes: int64(4096), VSizeBytes: int64(8192), Threads: 1}},
	) catch {
	case host.Malformed as err:
		return "ERR static: " + err.Reason
	}
	with context { host.System: staticHost } {
		let sOut = host.System.Status([]string{"/"}) catch {
		case host.Unavailable, host.Malformed:
			return "ERR static status"
		}
		let pOut = host.System.Processes() catch {
		case host.Unavailable, host.Malformed:
			return "ERR static procs"
		}
		if sOut.Cores != 4 || sOut.Volumes.Len() != 1 || pOut.Len() != 1 {
			return "ERR static read"
		}
		let found = host.FindProcess(pOut, 10) catch {
		case host.NotFound:
			return "ERR find process"
		}
		if host.CPUTicks(found) != int64(7) {
			return "ERR cputicks"
		}
	}
	let rawSpecial = "/mnt/dir name\tline\nnext\\slash"
	let escSpecial = host.EscapeMountField(rawSpecial)
	if !strings.Contains(escSpecial, "\\040") || !strings.Contains(escSpecial, "\\011") || !strings.Contains(escSpecial, "\\012") || !strings.Contains(escSpecial, "\\134") {
		return "ERR escape mountinfo: " + escSpecial
	}
	let decSpecial = host.UnescapeMountField(escSpecial) catch {
	case host.Malformed as err:
		return "ERR unescape mountinfo: " + err.Reason
	}
	if decSpecial != rawSpecial {
		return "ERR unescape roundtrip"
	}
	let miDoc = "10 1 8:1 / / rw - ext4 /dev/sda1 rw\n" +
		"11 10 8:1 / /mnt/rootbind rw - ext4 /dev/sda1 rw\n" +
		"12 10 0:33 /@ws /mnt/ws\\040dir rw,relatime shared:2 - btrfs /dev/loop0 rw,subvol=/@ws\n" +
		"13 10 0:33 /@tmp /mnt/ws\\040dir2 rw,relatime shared:3 - btrfs /dev/loop0 rw,subvol=/@tmp\n"
	let staticMountsProv = host.NewStaticMountinfo(
		miDoc,
		[]host.Volume{
			host.Volume{Path: "/", TotalBytes: int64(2000), FreeBytes: int64(500), AvailableBytes: int64(400), UsedBytes: int64(1500)},
			host.Volume{Path: "/mnt/ws dir", TotalBytes: int64(9000), FreeBytes: int64(3000), AvailableBytes: int64(2500), UsedBytes: int64(6000)},
		},
	) catch {
	case host.Malformed as err:
		return "ERR static mountinfo: " + err.Reason
	}
	with context { host.Volumes: staticMountsProv } {
		let ms = host.Volumes.Mounts() catch {
		case host.Unavailable, host.Malformed:
			return "ERR static mounts"
		}
		if ms.Len() != 4 || formatMountKind(ms[0].Info.Kind) != "primary" || formatMountKind(ms[1].Info.Kind) != "bind" || formatMountKind(ms[2].Info.Kind) != "subvol" || formatMountKind(ms[3].Info.Kind) != "subvol" {
			return "ERR static mount kinds"
		}
		let hit2 = host.Volumes.MountForPath("/mnt/ws dir2/sub") catch {
		case host.Unavailable, host.NotFound, host.Malformed:
			return "ERR mount for ws dir2"
		}
		if hit2.Info.MountPoint != "/mnt/ws dir2" {
			return "ERR mount boundary: " + hit2.Info.MountPoint
		}
		let groups = host.GroupByFilesystem(ms)
		if groups.Len() != 2 || !host.SameFilesystem(ms[2], ms[3]) || host.SameFilesystem(ms[0], ms[2]) {
			return "ERR group by filesystem"
		}
	}
	let customVol = host.Mount{
		Info: host.MountInfo{
			MountID:      1,
			ParentID:     0,
			DeviceID:     "9:9",
			Root:         "/",
			MountPoint:   "/custom",
			FSType:       "tmpfs",
			Source:       "tmpfs",
			Options:      []string{"rw"},
			SuperOptions: []string{"rw"},
			Kind:         host.PrimaryMount,
		},
		Volume: option.None,
	}
	with context { host.Volumes: customVolProv{mount: customVol} } {
		let gotCustom = host.Volumes.MountForPath("/custom/file") catch {
		case host.Unavailable, host.NotFound, host.Malformed:
			return "ERR custom Volumes provider"
		}
		if gotCustom.Info.DeviceID != "9:9" {
			return "ERR custom Volumes device"
		}
	}
	"OK"
}

func handle(line string) string needs host.System | host.Volumes | io.Stdout {
	let parts = strings.Fields(line)
	if parts.Len() == 0 {
		return ""
	}
	let cmd = parts[0]
	if cmd == "quit" {
		return "BYE"
	}
	if cmd == "laws" {
		return verifyPureNative()
	}
	if cmd == "status" {
		let vols = parts[1:]
		let st = host.System.Status(vols) catch {
		case host.Unavailable as err:
			return "ERR Unavailable \(err.Target): \(err.Reason)"
		case host.Malformed as err:
			return "ERR Malformed \(err.Source): \(err.Reason)"
		}
		if st.Volumes.Len() != vols.Len() {
			return "ERR volume count mismatch"
		}
		let mut volParts = []string{}
		for v in st.Volumes {
			volParts = volParts.Append(formatVol(v))
		}
		return "OK \(st.Cores) " + formatLoad(st.Load) + " " + formatMem(st.Memory) + " " + strings.Join(volParts, ";")
	}
	if cmd == "missing_vol" {
		let _ = host.System.Status([]string{parts[1]}) catch {
		case host.Unavailable as err:
			return "OK Unavailable " + err.Target
		case host.Malformed as err:
			return "ERR Malformed " + err.Source
		}
		return "ERR expected Unavailable"
	}
	if cmd == "self" {
		let pid = strconv.Atoi(parts[1]) catch {
		case strconv.Malformed, strconv.OutOfRange:
			return "ERR bad pid"
		}
		let ps = host.System.Processes() catch {
		case host.Unavailable as err:
			return "ERR Unavailable \(err.Target): \(err.Reason)"
		case host.Malformed as err:
			return "ERR Malformed \(err.Source): \(err.Reason)"
		}
		if !checkAllProcessesValid(ps) {
			return "ERR invalid process invariants"
		}
		let self = host.FindProcess(ps, pid) catch {
		case host.NotFound:
			return "ERR self missing"
		}
		let mut hexArgs = []string{}
		for a in self.Cmdline {
			hexArgs = hexArgs.Append(hex.Encode(a))
		}
		let _ = host.FindProcess(ps, -999999) catch {
		case host.NotFound:
			return "OK \(self.PID)|\(self.PPID)|\(self.State)|\(hex.Encode(self.Comm))|\(strings.Join(hexArgs, ","))|\(self.UserTicks)|\(self.SysTicks)|\(host.CPUTicks(self))|\(self.StartTicks)|\(self.RSSBytes)|\(self.VSizeBytes)|\(self.Threads)|\(ps.Len())"
		}
		return "ERR missing pid did not fail NotFound"
	}
	if cmd == "procs_ok" {
		let ps = host.System.Processes() catch {
		case host.Unavailable as err:
			return "ERR Unavailable \(err.Target): \(err.Reason)"
		case host.Malformed as err:
			return "ERR Malformed \(err.Source): \(err.Reason)"
		}
		if !checkAllProcessesValid(ps) {
			return "ERR invalid process invariants"
		}
		return "OK \(ps.Len())"
	}
	if cmd == "mounts" {
		let ms = host.Volumes.Mounts() catch {
		case host.Unavailable as err:
			return "ERR Unavailable \(err.Target): \(err.Reason)"
		case host.Malformed as err:
			return "ERR Malformed \(err.Source): \(err.Reason)"
		}
		let mut rows = []string{}
		for m in ms {
			if !host.ValidMount(m) {
				return "ERR invalid mount \(m.Info.MountPoint)"
			}
			rows = rows.Append(formatMount(m))
		}
		return "OK \(ms.Len())|\(strings.Join(rows, ";"))"
	}
	if cmd == "mount_for" {
		let argHex = if parts.Len() > 1 { parts[1] } else { "" }
		let target = hex.Decode(argHex) catch {
		case hex.Malformed:
			return "ERR bad hex target"
		}
		let m = host.Volumes.MountForPath(target) catch {
		case host.Unavailable as err:
			return "ERR Unavailable \(err.Target): \(err.Reason)"
		case host.NotFound as err:
			return "ERR NotFound \(err.Target)"
		case host.Malformed as err:
			return "ERR Malformed \(err.Source): \(err.Reason)"
		}
		return "OK \(formatMount(m))"
	}
	if cmd == "mount_groups" {
		let ms = host.Volumes.Mounts() catch {
		case host.Unavailable as err:
			return "ERR Unavailable \(err.Target): \(err.Reason)"
		case host.Malformed as err:
			return "ERR Malformed \(err.Source): \(err.Reason)"
		}
		let gs = host.GroupByFilesystem(ms)
		let mut rows = []string{}
		for g in gs {
			rows = rows.Append("\(hex.Encode(g.DeviceID))|\(hex.Encode(g.Primary.Info.MountPoint))|\(g.Mounts.Len())")
		}
		return "OK \(gs.Len())|\(strings.Join(rows, ";"))"
	}
	if cmd == "static_agrees" {
		let target = hex.Decode(parts[1]) catch {
		case hex.Malformed:
			return "ERR bad hex target"
		}
		let miRaw = hex.Decode(parts[2]) catch {
		case hex.Malformed:
			return "ERR bad hex mountinfo"
		}
		let live = host.Volumes.Mounts() catch {
		case host.Unavailable as err:
			return "ERR Unavailable \(err.Target): \(err.Reason)"
		case host.Malformed as err:
			return "ERR Malformed \(err.Source): \(err.Reason)"
		}
		let mut vols = []host.Volume{}
		for m in live {
			match m.Volume {
			case option.Some[host.Volume] as v:
				vols = vols.Append(v.Value)
			case option.None:
			}
		}
		let st = host.NewStaticMountinfo(miRaw, vols) catch {
		case host.Malformed as err:
			return "ERR Malformed static: \(err.Reason)"
		}
		let sim = st.Mounts() catch {
		case host.Unavailable, host.Malformed:
			return "ERR static mounts"
		}
		if live.Len() != sim.Len() {
			return "ERR mount count mismatch live=\(live.Len()) static=\(sim.Len())"
		}
		for i in 0..live.Len() {
			let a = live[i].Info
			let b = sim[i].Info
			if a.MountID != b.MountID || a.ParentID != b.ParentID || a.DeviceID != b.DeviceID || a.Root != b.Root || a.MountPoint != b.MountPoint || a.FSType != b.FSType || a.Source != b.Source || formatMountKind(a.Kind) != formatMountKind(b.Kind) {
				return "ERR mount row \(i) differs"
			}
		}
		let hitLive = host.Volumes.MountForPath(target) catch {
		case host.Unavailable, host.NotFound, host.Malformed:
			return "ERR live MountForPath"
		}
		let hitStatic = st.MountForPath(target) catch {
		case host.Unavailable, host.NotFound, host.Malformed:
			return "ERR static MountForPath"
		}
		if hitLive.Info.MountPoint != hitStatic.Info.MountPoint || hitLive.Info.DeviceID != hitStatic.Info.DeviceID || hitLive.Info.FSType != hitStatic.Info.FSType {
			return "ERR MountForPath disagrees"
		}
		if host.GroupByFilesystem(live).Len() != host.GroupByFilesystem(sim).Len() {
			return "ERR GroupByFilesystem count disagrees"
		}
		return "OK"
	}
	"ERR unknown"
}

func main() needs os.Ambient {
	let hostProv = host.NewHost()
	with context { host.System: hostProv, host.Volumes: hostProv, io.Stdout: io.NewHost(), os.Self: os.NewHost() } {
		io.Stdout.Println("READY \(os.Self.Pid())")
		for line in io.NewStdin().Lines() {
			let out = handle(line)
			if out != "" {
				io.Stdout.Println(out)
			}
			if line == "quit" {
				return
			}
		}
	}
}
`

var hostOracle struct {
	once sync.Once
	exe  string
	err  error
}

func buildHostOracle(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	hostOracle.once.Do(func() {
		root := moduleRootDir(t)
		dir, err := os.MkdirTemp("", "ergo-host-oracle-")
		if err != nil {
			hostOracle.err = err
			return
		}
		files := map[string]string{
			"mod/ergo.mod":             "module m\n",
			"mod/cmd/oracle/main.ergo": hostOracleSrc,
		}
		for rel, data := range files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				hostOracle.err = err
				return
			}
			if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
				hostOracle.err = err
				return
			}
		}
		ergo := filepath.Join(dir, "bin", "ergo")
		build := ergobuild.Command(ergo, "./cmd/ergo", "-buildvcs=false")
		build.Dir = root
		if out, err := runBound(build); err != nil {
			hostOracle.err = fmt.Errorf("build ergo: %v\n%s", err, out)
			return
		}
		exe := filepath.Join(dir, "bin", "oracle")
		compile := exec.Command(ergo, "build", "./cmd/oracle", "-o", exe)
		compile.Dir = filepath.Join(dir, "mod")
		compile.Env = append(os.Environ(), "ERGO_FALLBACK=off")
		if out, err := runBound(compile); err != nil {
			hostOracle.err = fmt.Errorf("ergo build host oracle: %v\n%s", err, out)
			return
		}
		hostOracle.exe = exe
	})
	if hostOracle.err != nil {
		t.Fatal(hostOracle.err)
	}
	return hostOracle.exe
}

type rawLoad struct {
	one, five, fifteen float64
	running, entities  int
}

func readRawLoadAvg(t *testing.T) rawLoad {
	t.Helper()
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		t.Fatal(err)
	}
	fs := strings.Fields(string(data))
	if len(fs) < 4 {
		t.Fatalf("raw /proc/loadavg: %q", string(data))
	}
	parseF := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatalf("parse float %q: %v", s, err)
		}
		return v
	}
	re := strings.Split(fs[3], "/")
	if len(re) != 2 {
		t.Fatalf("raw /proc/loadavg tasks: %q", fs[3])
	}
	r, err := strconv.Atoi(re[0])
	if err != nil {
		t.Fatal(err)
	}
	e, err := strconv.Atoi(re[1])
	if err != nil {
		t.Fatal(err)
	}
	return rawLoad{one: parseF(fs[0]), five: parseF(fs[1]), fifteen: parseF(fs[2]), running: r, entities: e}
}

type rawMem struct {
	total, avail, free int64
}

func readRawMeminfo(t *testing.T) rawMem {
	t.Helper()
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	var out rawMem
	var hasTotal, hasFree, hasAvail bool
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fs := strings.Fields(v)
		if len(fs) == 0 {
			continue
		}
		n, err := strconv.ParseInt(fs[0], 10, 64)
		if err != nil {
			continue
		}
		bytes := n * 1024
		switch strings.TrimSpace(k) {
		case "MemTotal":
			out.total = bytes
			hasTotal = true
		case "MemFree":
			out.free = bytes
			hasFree = true
		case "MemAvailable":
			out.avail = bytes
			hasAvail = true
		}
	}
	if !hasTotal || !hasFree || !hasAvail {
		t.Fatalf("/proc/meminfo missing keys: total=%v free=%v avail=%v", hasTotal, hasFree, hasAvail)
	}
	return out
}

type rawStat struct {
	pid, ppid, threads int
	state, comm        string
	utime, stime       int64
	starttime          int64
	vsize, rssPages    int64
}

func readRawProc(t *testing.T, pid int) (rawStat, int64, []string) {
	t.Helper()
	statRaw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	s := string(statRaw)
	open := strings.IndexByte(s, '(')
	closeIdx := strings.LastIndexByte(s, ')')
	if open <= 0 || closeIdx <= open {
		t.Fatalf("bad /proc/%d/stat: %q", pid, s)
	}
	pID, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil {
		t.Fatal(err)
	}
	rest := strings.Fields(s[closeIdx+1:])
	if len(rest) < 22 {
		t.Fatalf("short /proc/%d/stat fields: %d", pid, len(rest))
	}
	atoi := func(x string) int {
		v, err := strconv.Atoi(x)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	atoll := func(x string) int64 {
		v, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	st := rawStat{
		pid:       pID,
		comm:      s[open+1 : closeIdx],
		state:     rest[0],
		ppid:      atoi(rest[1]),
		utime:     atoll(rest[11]),
		stime:     atoll(rest[12]),
		threads:   atoi(rest[17]),
		starttime: atoll(rest[19]),
		vsize:     atoll(rest[20]),
		rssPages:  atoll(rest[21]),
	}
	statmRaw, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		t.Fatal(err)
	}
	statmFields := strings.Fields(string(statmRaw))
	if len(statmFields) < 2 {
		t.Fatalf("bad /proc/%d/statm: %q", pid, string(statmRaw))
	}
	resPages := atoll(statmFields[1])
	cmdRaw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		t.Fatal(err)
	}
	trimmed := bytes.TrimRight(cmdRaw, "\x00")
	var args []string
	if len(trimmed) > 0 {
		for _, part := range bytes.Split(trimmed, []byte{0}) {
			args = append(args, string(part))
		}
	}
	return st, resPages * int64(os.Getpagesize()), args
}

// TestStdHostAgreesWithProc builds the live oracle binary with ERGO_FALLBACK=off
// and verifies every reading from std/os/host (Status and Processes) agrees with raw
// /proc/loadavg, /proc/meminfo, runtime.NumCPU(), syscall.Statfs, and
// /proc/<pid>/{stat,statm,cmdline} within explicit stated tolerances, as well as
// vanishing-PID tolerance under concurrent subprocess churn.
func TestStdHostAgreesWithProc(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux /proc agreement law runs on linux only")
	}
	exe := buildHostOracle(t)
	tmpDir := t.TempDir()

	cmd := exec.Command(exe, "--probe", "arg with spaces")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	readLine := func() string {
		t.Helper()
		if !sc.Scan() {
			t.Fatalf("oracle closed stdout early (err=%v stderr=%q)", sc.Err(), stderr.String())
		}
		return sc.Text()
	}
	send := func(line string) string {
		t.Helper()
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatalf("write oracle %q: %v", line, err)
		}
		return readLine()
	}

	ready := readLine()
	if !strings.HasPrefix(ready, "READY ") {
		t.Fatalf("unexpected greeting: %q", ready)
	}
	oraclePID, err := strconv.Atoi(strings.TrimPrefix(ready, "READY "))
	if err != nil || oraclePID != cmd.Process.Pid {
		t.Fatalf("oracle pid=%q, want %d", ready, cmd.Process.Pid)
	}

	if resp := send("laws"); resp != "OK" {
		t.Fatalf("native ERGO_FALLBACK=off pure/static checks failed: %q", resp)
	}

	// 1. Live Status vs /proc/loadavg, /proc/meminfo, runtime.NumCPU, syscall.Statfs.
	// Tolerances accommodate concurrent system activity during bracketed reads:
	// - Cores: exact == runtime.NumCPU()
	// - LoadAvg: within [min-1.0, max+1.0] across before/after /proc/loadavg reads; Entities +-256
	// - Memory.TotalBytes: exact == raw MemTotal; Free/Available/Used within +-128 MiB across before/after
	// - Volume.TotalBytes: exact == raw Statfs; Free/Available/Used within +-128 MiB across before/after
	loadBefore := readRawLoadAvg(t)
	memBefore := readRawMeminfo(t)
	v0TotalBefore, v0FreeBefore, v0AvailBefore, err := rawStatfs("/")
	if err != nil {
		t.Fatal(err)
	}
	v1TotalBefore, v1FreeBefore, v1AvailBefore, err := rawStatfs(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	statusResp := send("status / " + tmpDir)

	loadAfter := readRawLoadAvg(t)
	memAfter := readRawMeminfo(t)
	v0TotalAfter, v0FreeAfter, v0AvailAfter, err := rawStatfs("/")
	if err != nil {
		t.Fatal(err)
	}
	v1TotalAfter, v1FreeAfter, v1AvailAfter, err := rawStatfs(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(statusResp, "OK ") {
		t.Fatalf("status failed: %q", statusResp)
	}
	sFields := strings.Fields(statusResp)
	if len(sFields) != 5 {
		t.Fatalf("unexpected status response: %q", statusResp)
	}
	cores, err := strconv.Atoi(sFields[1])
	if err != nil {
		t.Fatal(err)
	}
	if want := runtime.NumCPU(); cores != want {
		t.Errorf("Status.Cores = %d, want runtime.NumCPU() = %d", cores, want)
	}

	loadParts := strings.Split(sFields[2], "|")
	if len(loadParts) != 5 {
		t.Fatalf("bad load parts: %q", sFields[2])
	}
	parseF := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	parseI := func(s string) int {
		v, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	parseI64 := func(s string) int64 {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	gotLoad := rawLoad{
		one:      parseF(loadParts[0]),
		five:     parseF(loadParts[1]),
		fifteen:  parseF(loadParts[2]),
		running:  parseI(loadParts[3]),
		entities: parseI(loadParts[4]),
	}
	checkLoadWindow := func(name string, got, b, a float64) {
		lo := math.Min(b, a) - 1.0
		hi := math.Max(b, a) + 1.0
		if got < lo || got > hi {
			t.Errorf("Load.%s = %v, outside [%v, %v] (before=%v after=%v)", name, got, lo, hi, b, a)
		}
	}
	checkLoadWindow("One", gotLoad.one, loadBefore.one, loadAfter.one)
	checkLoadWindow("Five", gotLoad.five, loadBefore.five, loadAfter.five)
	checkLoadWindow("Fifteen", gotLoad.fifteen, loadBefore.fifteen, loadAfter.fifteen)
	if gotLoad.running < 0 {
		t.Errorf("Load.Running = %d, want >= 0", gotLoad.running)
	}
	minEnt := min(loadBefore.entities, loadAfter.entities) - 256
	maxEnt := max(loadBefore.entities, loadAfter.entities) + 256
	if gotLoad.entities < minEnt || gotLoad.entities > maxEnt {
		t.Errorf("Load.Entities = %d, outside [%d, %d]", gotLoad.entities, minEnt, maxEnt)
	}

	memParts := strings.Split(sFields[3], "|")
	if len(memParts) != 4 {
		t.Fatalf("bad mem parts: %q", sFields[3])
	}
	memTotal := parseI64(memParts[0])
	memAvail := parseI64(memParts[1])
	memFree := parseI64(memParts[2])
	memUsed := parseI64(memParts[3])
	if memTotal != memBefore.total || memTotal != memAfter.total {
		t.Errorf("Memory.TotalBytes = %d, want %d", memTotal, memBefore.total)
	}
	if memUsed+memAvail != memTotal || memFree > memTotal || memAvail > memTotal || memFree < 0 || memAvail < 0 || memUsed < 0 {
		t.Errorf("Memory invariant violated: total=%d avail=%d free=%d used=%d", memTotal, memAvail, memFree, memUsed)
	}
	const memTol = int64(128 << 20)
	checkMemWindow := func(name string, got, b, a int64) {
		lo := min(b, a) - memTol
		hi := max(b, a) + memTol
		if got < lo || got > hi {
			t.Errorf("Memory.%s = %d, outside [%d, %d] (before=%d after=%d)", name, got, lo, hi, b, a)
		}
	}
	checkMemWindow("AvailableBytes", memAvail, memBefore.avail, memAfter.avail)
	checkMemWindow("FreeBytes", memFree, memBefore.free, memAfter.free)
	checkMemWindow("UsedBytes", memUsed, memBefore.total-memBefore.avail, memAfter.total-memAfter.avail)

	volParts := strings.Split(sFields[4], ";")
	if len(volParts) != 2 {
		t.Fatalf("expected 2 volumes, got %v", volParts)
	}
	checkVolume := func(encoded, wantPath string, totB, freeB, availB, totA, freeA, availA int64) {
		vp := strings.Split(encoded, "|")
		if len(vp) != 5 {
			t.Fatalf("bad volume encoding: %q", encoded)
		}
		pb, err := hex.DecodeString(vp[0])
		if err != nil {
			t.Fatal(err)
		}
		if string(pb) != wantPath {
			t.Errorf("Volume.Path = %q, want %q", string(pb), wantPath)
		}
		tot := parseI64(vp[1])
		free := parseI64(vp[2])
		avail := parseI64(vp[3])
		used := parseI64(vp[4])
		if tot != totB || tot != totA {
			t.Errorf("Volume(%s).TotalBytes = %d, want %d", wantPath, tot, totB)
		}
		if used+free != tot || free > tot || avail > tot || free < 0 || avail < 0 || used < 0 {
			t.Errorf("Volume(%s) invariant violated: tot=%d free=%d avail=%d used=%d", wantPath, tot, free, avail, used)
		}
		const fsTol = int64(128 << 20)
		if free < min(freeB, freeA)-fsTol || free > max(freeB, freeA)+fsTol {
			t.Errorf("Volume(%s).FreeBytes = %d outside [%d, %d]", wantPath, free, min(freeB, freeA)-fsTol, max(freeB, freeA)+fsTol)
		}
		if avail < min(availB, availA)-fsTol || avail > max(availB, availA)+fsTol {
			t.Errorf("Volume(%s).AvailableBytes = %d outside [%d, %d]", wantPath, avail, min(availB, availA)-fsTol, max(availB, availA)+fsTol)
		}
		usedB, usedA := totB-freeB, totA-freeA
		if used < min(usedB, usedA)-fsTol || used > max(usedB, usedA)+fsTol {
			t.Errorf("Volume(%s).UsedBytes = %d outside [%d, %d]", wantPath, used, min(usedB, usedA)-fsTol, max(usedB, usedA)+fsTol)
		}
	}
	checkVolume(volParts[0], "/", v0TotalBefore, v0FreeBefore, v0AvailBefore, v0TotalAfter, v0FreeAfter, v0AvailAfter)
	checkVolume(volParts[1], tmpDir, v1TotalBefore, v1FreeBefore, v1AvailBefore, v1TotalAfter, v1FreeAfter, v1AvailAfter)

	if resp := send("missing_vol " + filepath.Join(tmpDir, "no-such-volume")); !strings.HasPrefix(resp, "OK Unavailable ") {
		t.Errorf("missing volume response = %q, want OK Unavailable ...", resp)
	}

	// 2. Live Processes() vs /proc/<self>/{stat,statm,cmdline} while running.
	statBefore, rssBefore, cmdBefore := readRawProc(t, oraclePID)
	selfResp := send(fmt.Sprintf("self %d", oraclePID))
	statAfter, rssAfter, cmdAfter := readRawProc(t, oraclePID)
	if !strings.HasPrefix(selfResp, "OK ") {
		t.Fatalf("self failed: %q", selfResp)
	}
	pf := strings.Split(strings.TrimPrefix(selfResp, "OK "), "|")
	if len(pf) != 13 {
		t.Fatalf("bad self fields: %q", selfResp)
	}
	gotPID := parseI(pf[0])
	gotPPID := parseI(pf[1])
	gotState := pf[2]
	commBytes, err := hex.DecodeString(pf[3])
	if err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	if pf[4] != "" {
		for _, hx := range strings.Split(pf[4], ",") {
			b, err := hex.DecodeString(hx)
			if err != nil {
				t.Fatal(err)
			}
			gotArgs = append(gotArgs, string(b))
		}
	}
	gotUserTicks := parseI64(pf[5])
	gotSysTicks := parseI64(pf[6])
	gotTotalTicks := parseI64(pf[7])
	gotStartTicks := parseI64(pf[8])
	gotRSS := parseI64(pf[9])
	gotVSize := parseI64(pf[10])
	gotThreads := parseI(pf[11])
	gotProcCount := parseI(pf[12])

	if gotPID != oraclePID || gotPPID != os.Getpid() {
		t.Errorf("PID/PPID = (%d, %d), want (%d, %d)", gotPID, gotPPID, oraclePID, os.Getpid())
	}
	if len(gotState) != 1 || !strings.Contains("RSDZTtWXI", gotState) {
		t.Errorf("State = %q, want single Linux state code", gotState)
	}
	if string(commBytes) != statBefore.comm {
		t.Errorf("Comm = %q, want %q", string(commBytes), statBefore.comm)
	}
	wantCmdline := []string{exe, "--probe", "arg with spaces"}
	if strings.Join(gotArgs, "\x00") != strings.Join(cmdBefore, "\x00") ||
		strings.Join(gotArgs, "\x00") != strings.Join(cmdAfter, "\x00") ||
		strings.Join(gotArgs, "\x00") != strings.Join(wantCmdline, "\x00") {
		t.Errorf("Cmdline = %q, want raw=%q and argv=%q", gotArgs, cmdBefore, wantCmdline)
	}
	if gotStartTicks != statBefore.starttime || gotStartTicks != statAfter.starttime {
		t.Errorf("StartTicks = %d, want %d", gotStartTicks, statBefore.starttime)
	}
	if gotUserTicks < statBefore.utime || gotUserTicks > statAfter.utime+200 {
		t.Errorf("UserTicks = %d outside [%d, %d]", gotUserTicks, statBefore.utime, statAfter.utime+200)
	}
	if gotSysTicks < statBefore.stime || gotSysTicks > statAfter.stime+200 {
		t.Errorf("SysTicks = %d outside [%d, %d]", gotSysTicks, statBefore.stime, statAfter.stime+200)
	}
	if gotTotalTicks != gotUserTicks+gotSysTicks {
		t.Errorf("CPUTicks = %d, want %d+%d", gotTotalTicks, gotUserTicks, gotSysTicks)
	}
	const rssTol = int64(32 << 20)
	if gotRSS <= 0 || gotRSS < min(rssBefore, rssAfter)-rssTol || gotRSS > max(rssBefore, rssAfter)+rssTol {
		t.Errorf("RSSBytes = %d outside [%d, %d]", gotRSS, min(rssBefore, rssAfter)-rssTol, max(rssBefore, rssAfter)+rssTol)
	}
	const vsizeTol = int64(64 << 20)
	if gotVSize <= 0 || gotVSize < min(statBefore.vsize, statAfter.vsize)-vsizeTol || gotVSize > max(statBefore.vsize, statAfter.vsize)+vsizeTol {
		t.Errorf("VSizeBytes = %d outside [%d, %d]", gotVSize, min(statBefore.vsize, statAfter.vsize)-vsizeTol, max(statBefore.vsize, statAfter.vsize)+vsizeTol)
	}
	if gotThreads < 1 || gotThreads < min(statBefore.threads, statAfter.threads)-8 || gotThreads > max(statBefore.threads, statAfter.threads)+8 {
		t.Errorf("Threads = %d outside [%d, %d]", gotThreads, min(statBefore.threads, statAfter.threads)-8, max(statBefore.threads, statAfter.threads)+8)
	}
	if gotProcCount < 1 {
		t.Errorf("process count = %d, want >= 1", gotProcCount)
	}

	// 3. Vanishing PID tolerance: churn short-lived processes during /proc scans.
	var stop atomic.Bool
	var wg sync.WaitGroup
	if trueBin, err := exec.LookPath("true"); err == nil {
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for !stop.Load() {
					_ = exec.Command(trueBin).Run()
				}
			}()
		}
	}
	for i := range 10 {
		if resp := send("procs_ok"); !strings.HasPrefix(resp, "OK ") {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("Processes() failed during PID churn iter %d: %q", i, resp)
		}
	}
	stop.Store(true)
	wg.Wait()

	if resp := send("quit"); resp != "BYE" {
		t.Errorf("quit response = %q, want BYE", resp)
	}
}

type rawMountEntry struct {
	mountID    int
	parentID   int
	deviceID   string
	root       string
	mountPoint string
	fsType     string
	source     string
	kind       string
}

func unescapeGoMountField(s string) (string, error) {
	if !strings.ContainsRune(s, '\\') {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+3 >= len(s) {
			return "", fmt.Errorf("truncated octal escape in %q", s)
		}
		var val int
		for j := 1; j <= 3; j++ {
			c := s[i+j]
			if c < '0' || c > '7' {
				return "", fmt.Errorf("invalid octal digit in %q", s)
			}
			val = val*8 + int(c-'0')
		}
		if val == 0 || val > 255 {
			return "", fmt.Errorf("out-of-range octal escape in %q", s)
		}
		b.WriteByte(byte(val))
		i += 4
	}
	return b.String(), nil
}

func parseGoMountinfo(t *testing.T, text string) []rawMountEntry {
	t.Helper()
	var out []rawMountEntry
	seen := map[string]bool{}
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' })
		if len(fields) < 10 {
			t.Fatalf("short mountinfo line: %q", line)
		}
		id, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatalf("bad mount id %q: %v", fields[0], err)
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("bad parent id %q: %v", fields[1], err)
		}
		root, err := unescapeGoMountField(fields[3])
		if err != nil {
			t.Fatal(err)
		}
		mp, err := unescapeGoMountField(fields[4])
		if err != nil {
			t.Fatal(err)
		}
		dash := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				dash = i
				break
			}
		}
		if dash < 0 || len(fields) != dash+4 {
			t.Fatalf("missing or misaligned '-' in %q", line)
		}
		fsType, err := unescapeGoMountField(fields[dash+1])
		if err != nil {
			t.Fatal(err)
		}
		src, err := unescapeGoMountField(fields[dash+2])
		if err != nil {
			t.Fatal(err)
		}
		superOpts := strings.Split(fields[dash+3], ",")
		var subvol string
		for _, opt := range superOpts {
			optDec, err := unescapeGoMountField(opt)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(optDec, "subvol=") {
				subvol = strings.TrimPrefix(optDec, "subvol=")
				if !strings.HasPrefix(subvol, "/") {
					subvol = "/" + subvol
				}
				subvol = filepath.Clean(subvol)
				break
			}
		}
		cleanRoot := root
		if !strings.HasPrefix(cleanRoot, "/") {
			cleanRoot = "/" + cleanRoot
		}
		cleanRoot = filepath.Clean(cleanRoot)
		key := fields[2] + "\x00" + cleanRoot
		kind := "primary"
		if seen[key] {
			kind = "bind"
		} else if fsType == "btrfs" && subvol != "" {
			if cleanRoot == subvol && cleanRoot != "/" {
				kind = "subvol"
			} else if cleanRoot != subvol {
				kind = "bind"
			}
		} else if cleanRoot != "/" {
			kind = "bind"
		}
		seen[key] = true
		out = append(out, rawMountEntry{
			mountID:    id,
			parentID:   pid,
			deviceID:   fields[2],
			root:       root,
			mountPoint: filepath.Clean(mp),
			fsType:     fsType,
			source:     src,
			kind:       kind,
		})
	}
	return out
}

// TestStdHostMountsAgrees compiles an Ergo binary using os/host.Volumes with
// ERGO_FALLBACK=off and checks mount enumeration, /proc/self/mountinfo
// agreement, Statfs volume capacity agreement, longest-prefix resolution,
// DeviceID grouping, and Static vs Host agreement on Linux.
func TestStdHostMountsAgrees(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux /proc/self/mountinfo agreement test runs on linux only")
	}
	exe := buildHostOracle(t)
	workDir := t.TempDir()

	cmd := exec.Command(exe, "--mounts")
	cmd.Dir = workDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	send := func(line string) string {
		t.Helper()
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatalf("write oracle %q: %v", line, err)
		}
		if !sc.Scan() {
			t.Fatalf("oracle closed stdout early (err=%v stderr=%q)", sc.Err(), stderr.String())
		}
		return sc.Text()
	}

	ready := func() string {
		if !sc.Scan() {
			t.Fatalf("oracle exited before READY (err=%v stderr=%q)", sc.Err(), stderr.String())
		}
		return sc.Text()
	}()
	if !strings.HasPrefix(ready, "READY ") {
		t.Fatalf("unexpected greeting: %q", ready)
	}
	oraclePID, err := strconv.Atoi(strings.TrimPrefix(ready, "READY "))
	if err != nil {
		t.Fatalf("bad pid in greeting %q: %v", ready, err)
	}

	if resp := send("laws"); resp != "OK" {
		t.Fatalf("native pure mount laws = %q, want OK", resp)
	}

	rawMIBefore, err := os.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", oraclePID))
	if err != nil {
		t.Fatal(err)
	}
	resp := send("mounts")
	rawMIAfter, err := os.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", oraclePID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp, "OK ") {
		t.Fatalf("mounts failed: %q", resp)
	}
	payload := strings.TrimPrefix(resp, "OK ")
	countStr, rowsRaw, ok := strings.Cut(payload, "|")
	if !ok {
		t.Fatalf("malformed mounts payload: %q", resp)
	}
	gotCount, err := strconv.Atoi(countStr)
	if err != nil {
		t.Fatalf("bad mount count %q: %v", countStr, err)
	}
	gotRows := strings.Split(rowsRaw, ";")
	if len(gotRows) != gotCount || gotCount == 0 {
		t.Fatalf("mount row count %d != reported %d", len(gotRows), gotCount)
	}

	// Only enforce exact row-by-row equality when the host mount table did not
	// mutate mid-call; invariants still hold unconditionally.
	tableStable := bytes.Equal(rawMIBefore, rawMIAfter)
	wantEntries := parseGoMountinfo(t, string(rawMIAfter))
	if tableStable && len(wantEntries) != gotCount {
		t.Fatalf("host.Mounts() count = %d, raw /proc/%d/mountinfo count = %d", gotCount, oraclePID, len(wantEntries))
	}

	decodeHexStr := func(h string) string {
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatalf("bad hex %q: %v", h, err)
		}
		return string(b)
	}
	const blockTol = int64(256 << 20)
	delta64 := func(a, b int64) int64 {
		if a > b {
			return a - b
		}
		return b - a
	}
	sawProcNone := false
	devCounts := map[string]int{}
	for i, row := range gotRows {
		parts := strings.Split(row, "|")
		if len(parts) != 9 {
			t.Fatalf("bad mount row %d: %q", i, row)
		}
		mid, _ := strconv.Atoi(parts[0])
		pid, _ := strconv.Atoi(parts[1])
		devID := decodeHexStr(parts[2])
		root := decodeHexStr(parts[3])
		mp := decodeHexStr(parts[4])
		fsType := decodeHexStr(parts[5])
		src := decodeHexStr(parts[6])
		kind := parts[7]
		volSpec := parts[8]
		devCounts[devID]++

		if tableStable {
			w := wantEntries[i]
			if mid != w.mountID || pid != w.parentID || devID != w.deviceID || root != w.root || mp != w.mountPoint || fsType != w.fsType || src != w.source || kind != w.kind {
				t.Fatalf("mount row %d got (%d,%d,%s,%s,%s,%s,%s,%s), want %+v",
					i, mid, pid, devID, root, mp, fsType, src, kind, w)
			}
		}
		if mp == "/proc" && fsType == "proc" && volSpec == "NONE" {
			sawProcNone = true
		}
		if mp == "/" || mp == "/tmp" || mp == "/workspaces" {
			if volSpec == "NONE" {
				t.Fatalf("expected non-empty Volume for %s", mp)
			}
			vp := strings.Split(volSpec, ":")
			if len(vp) != 4 {
				t.Fatalf("bad volume spec for %s: %q", mp, volSpec)
			}
			tot, _ := strconv.ParseInt(vp[0], 10, 64)
			free, _ := strconv.ParseInt(vp[1], 10, 64)
			avail, _ := strconv.ParseInt(vp[2], 10, 64)
			used, _ := strconv.ParseInt(vp[3], 10, 64)
			wt, wf, wa, err := rawStatfs(mp)
			if err != nil {
				t.Fatalf("rawStatfs(%s): %v", mp, err)
			}
			if tot != wt || used != tot-free || delta64(free, wf) > blockTol || delta64(avail, wa) > blockTol {
				t.Fatalf("mount %s Volume (%d,%d,%d,%d) disagrees with syscall.Statfs (%d,%d,%d)",
					mp, tot, free, avail, used, wt, wf, wa)
			}
		}
	}
	if !sawProcNone {
		t.Log("note: /proc did not report Volume=NONE (unusual host /proc)")
	}

	// Verify MountForPath agreement on "/", workDir, relative ".", and empty "".
	for _, query := range []string{"/", workDir, "."} {
		r := send("mount_for " + hex.EncodeToString([]byte(query)))
		if !strings.HasPrefix(r, "OK ") {
			t.Fatalf("mount_for %q failed: %q", query, r)
		}
		resolvedTarget := query
		if query == "." {
			resolvedTarget = workDir
		}
		row := strings.Split(strings.TrimPrefix(r, "OK "), "|")
		if len(row) != 9 {
			t.Fatalf("bad mount_for row for %q: %q", query, r)
		}
		gotMP := decodeHexStr(row[4])
		var bestMP, bestDev string
		for _, w := range wantEntries {
			matches := w.mountPoint == "/" || resolvedTarget == w.mountPoint || strings.HasPrefix(resolvedTarget, w.mountPoint+"/")
			if matches && len(w.mountPoint) >= len(bestMP) {
				bestMP = w.mountPoint
				bestDev = w.deviceID
			}
		}
		if tableStable && (gotMP != bestMP || decodeHexStr(row[2]) != bestDev) {
			t.Fatalf("mount_for %q got (%s, %s), want (%s, %s)", query, gotMP, decodeHexStr(row[2]), bestMP, bestDev)
		}
	}
	if r := send("mount_for "); !strings.HasPrefix(r, "ERR Malformed") {
		t.Fatalf("mount_for empty = %q, want ERR Malformed", r)
	}

	// Verify GroupByFilesystem matches Go DeviceID grouping count and member total.
	gr := send("mount_groups")
	if !strings.HasPrefix(gr, "OK ") {
		t.Fatalf("mount_groups failed: %q", gr)
	}
	gCountStr, gRowsRaw, ok := strings.Cut(strings.TrimPrefix(gr, "OK "), "|")
	if !ok {
		t.Fatalf("malformed mount_groups payload: %q", gr)
	}
	gCount, err := strconv.Atoi(gCountStr)
	if err != nil {
		t.Fatal(err)
	}
	gRows := strings.Split(gRowsRaw, ";")
	if len(gRows) != gCount {
		t.Fatalf("group rows %d != count %d", len(gRows), gCount)
	}
	if tableStable && gCount != len(devCounts) {
		t.Fatalf("GroupByFilesystem count = %d, distinct DeviceIDs = %d", gCount, len(devCounts))
	}
	sumMembers := 0
	for _, grow := range gRows {
		gp := strings.Split(grow, "|")
		if len(gp) != 3 {
			t.Fatalf("bad group row: %q", grow)
		}
		n, _ := strconv.Atoi(gp[2])
		sumMembers += n
	}
	if sumMembers != gotCount && tableStable {
		t.Fatalf("grouped mounts sum %d != total mounts %d", sumMembers, gotCount)
	}

	// Verify Static (NewStaticMountinfo) agrees with live Host on same /proc/self/mountinfo.
	staticResp := send("static_agrees " + hex.EncodeToString([]byte(workDir)) + " " + hex.EncodeToString(rawMIAfter))
	if staticResp != "OK" {
		t.Fatalf("static_agrees = %q, want OK", staticResp)
	}

	if resp := send("quit"); resp != "BYE" {
		t.Errorf("quit response = %q, want BYE", resp)
	}
}

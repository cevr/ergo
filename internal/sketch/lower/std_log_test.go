package lower

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/testwait"
)

// TestStdLogLawsRun runs the log probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed. It builds Go because the laws run tasks' context values and a
// sync.Ref, which only the runtime has.
func TestStdLogLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdlog", stdLogLawsProbe, stdLogLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

// logSlogProgram reads one entry per line, "level step message key text
// …" with each text in hex, moves a Manual clock on by step nanoseconds,
// and logs the entry through log.JSONLines (least level Info) to standard
// error.
const logSlogProgram = `import hex
import io
import log
import strconv
import strings
import time

func num(s string) int64 {
	strconv.ParseInt(s, 10, 64) catch {
	case strconv.Malformed:
		0
	case strconv.OutOfRange:
		0
	}
}

func text(s string) string {
	hex.Decode(s) catch {
	case hex.Malformed:
		"?"
	}
}

func level(s string) log.Level {
	if s == "D" {
		log.LevelDebug
	} else if s == "I" {
		log.LevelInfo
	} else if s == "W" {
		log.LevelWarn
	} else {
		log.LevelError
	}
}

func main() needs Ambient {
	let m = time.NewManual(1700000000)
	let sink = log.NewJSONLines(io.NewErrors(), m, log.LevelInfo)
	with context { log.Sink: sink } {
		for line in io.NewStdin().Lines() {
			let f = strings.Split(line, " ")
			m.Advance(duration(num(f[1])))
			let mut attrs = []log.Attr{}
			for i in 0..(len(f) - 3) / 2 {
				attrs = attrs.Append(log.Attr{Key: text(f[3 + 2 * i]), Text: text(f[4 + 2 * i])})
			}
			log.Log(level(f[0]), text(f[2]), attrs)
		}
	}
}
`

// TestStdLogMatchesSlog (std-spike C3): log.JSONLines writes what Go's
// slog.JSONHandler writes for the same entries, least level and times, on
// 2,000 seeded entries of every level with zero to three attributes whose
// texts draw from quotes, backslashes, control bytes, <, > and &, U+2028
// and multi-byte runes (valid UTF-8: for a byte of invalid UTF-8 json's
// surrogate escape departs from slog's U+FFFD on purpose). It catches what
// the laws do not: a member, key, level name, time layout or escape that
// differs from slog's (mutants c3-time-seconds, c3-level-name, c3-msg-key,
// c3-call-attrs-dropped, c3-rank-swap). ERGO_LOG_SLOG_SEED replays a seed.
//
// It builds Go because it compares the run of a built program with Go's
// own package, and the question is behaviour at run time; no in-process
// check observes that, and Ergo has no reference interpreter yet
// (test-speed T8).
func TestStdLogMatchesSlog(t *testing.T) {
	needsBuild(t)
	seed := uint64(3)
	if s := os.Getenv("ERGO_LOG_SLOG_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_LOG_SLOG_SEED=%q: %v", s, err)
		}
		seed = n
	}
	replay := fmt.Sprintf("replay: ERGO_LOG_SLOG_SEED=%d go test -run 'TestStdLogMatchesSlog$' ./internal/sketch/lower", seed)
	exe := tableExes(t, "logSlog")["logSlog"]
	probeRan(exe)
	cases, want := slogCases(rand.New(rand.NewPCG(seed, 17)), 2000)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Stdin = strings.NewReader(strings.Join(cases, "\n") + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProgram(t, cmd) // under the test kit's memory limit (proclimit)
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, firstLines(stderr.String(), 20), replay)
	}
	got := strings.SplitAfter(stderr.String(), "\n")
	wants := strings.SplitAfter(want, "\n")
	for i := range max(len(got), len(wants)) {
		g, w := "", ""
		if i < len(got) {
			g = got[i]
		}
		if i < len(wants) {
			w = wants[i]
		}
		if g != w {
			t.Fatalf("line %d differs:\nErgo: %q\nslog: %q\n%s", i+1, g, w, replay)
		}
	}
}

// slogCases are n entries for logSlogProgram and slog's JSON lines of
// them, with the least level Info.
func slogCases(r *rand.Rand, n int) (cases []string, want string) {
	alphabet := []string{"a", "Z", "0", " ", "\"", "\\", "/", "\n", "\r", "\t", "\x00", "\x01", "\x1f", "\x7f", "<", ">", "&", "\u2028", "\u2029", "é", "€", "😀", "_tag", "{", "}"}
	draw := func() string {
		var b strings.Builder
		for range r.IntN(6) {
			b.WriteString(alphabet[r.IntN(len(alphabet))])
		}
		return b.String()
	}
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	levels := []struct {
		code  string
		level slog.Level
	}{{"D", slog.LevelDebug}, {"I", slog.LevelInfo}, {"W", slog.LevelWarn}, {"E", slog.LevelError}}
	at := time.Unix(1700000000, 0).UTC()
	for range n {
		lv := levels[r.IntN(len(levels))]
		step := r.Int64N(int64(3 * time.Second))
		at = at.Add(time.Duration(step))
		msg := draw()
		fields := []string{lv.code, strconv.FormatInt(step, 10), hex.EncodeToString([]byte(msg))}
		var attrs []slog.Attr
		for range r.IntN(4) {
			k, v := "k"+draw(), draw()
			fields = append(fields, hex.EncodeToString([]byte(k)), hex.EncodeToString([]byte(v)))
			attrs = append(attrs, slog.String(k, v))
		}
		cases = append(cases, strings.Join(fields, " "))
		if h.Enabled(context.Background(), lv.level) {
			rec := slog.NewRecord(at, lv.level, msg, 0)
			rec.AddAttrs(attrs...)
			_ = h.Handle(context.Background(), rec)
		}
	}
	return cases, buf.String()
}

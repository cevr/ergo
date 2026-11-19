package lower

// Generated from the laws of internal/sketch/std/log by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdLogLawsProbe restates the log laws whose runner is
// TestStdLogLawsRun as Ergo tests over std log.
const stdLogLawsProbe = `import io
import log
import time

// levelAt restates log's levelAt: each of the four levels for n mod 4.
func levelAt(n int) log.Level {
	let k = ((n % 4) + 4) % 4
	if k == 0 {
		log.LevelDebug
	} else if k == 1 {
		log.LevelInfo
	} else if k == 2 {
		log.LevelWarn
	} else {
		log.LevelError
	}
}

// monotone is the member law Sink.EnabledIsMonotone over the sink s.
func monotone(s log.Sink, a int, b int) bool {
	let lo = levelAt(a)
	let hi = levelAt(b)
	!s.Enabled(lo) || !log.Admits(lo, hi) || s.Enabled(hi)
}

// The member law Sink.EnabledIsMonotone, over each std provider made with
// each least level.
test LogSinkEnabledIsMonotone(least int, a int, b int) {
	let r = log.NewRecorder(levelAt(least))
	let j = log.NewJSONLines(io.NewRecorder(), time.NewManual(0), levelAt(least))
	assert monotone(r, a, b) && monotone(j, a, b)
}

test LogAnnotationsNest(a string, b string, c string) {
	let before = log.Annotations()
	let inner = with log.Annotate("outer", a) {
		with log.Annotate("inner", b) {
			with log.Annotate("outer", c) {
				log.Annotations()
			}
		}
	}
	let after = log.Annotations()
	assert inner == []log.Attr{log.Attr{Key: "inner", Text: b}, log.Attr{Key: "outer", Text: c}} && after == before
}

test LogLevelFilters(least int, at int, message string, text string) {
	let r = log.NewRecorder(levelAt(least))
	with context { log.Sink: r } {
		with log.Annotate("k", text) {
			log.Log(levelAt(at), message, []log.Attr{})
		}
	}
	let mut want = []log.Entry{}
	if log.Admits(levelAt(least), levelAt(at)) {
		want = want.Append(log.Entry{Level: levelAt(at), Message: message, Attrs: []log.Attr{log.Attr{Key: "k", Text: text}}})
	}
	assert r.Entries() == want
}
`

// stdLogLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdLogLawStatements = map[string]string{
	"log Sink.EnabledIsMonotone": `EnabledIsMonotone(a int, b int) { let lo = levelAt(a) let hi = levelAt(b) !Enabled(lo) || !Admits(lo, hi) || Enabled(hi) }`,
	"log AnnotationsNest":        `AnnotationsNest(a string, b string, c string) { let before = Annotations() let inner = with Annotate("outer", a) { with Annotate("inner", b) { with Annotate("outer", c) { Annotations() } } } let after = Annotations() inner == []Attr{Attr{Key: "inner", Text: b}, Attr{Key: "outer", Text: c}} && after == before }`,
	"log LevelFilters":           `LevelFilters(least int, at int, message string, text string) { let r = NewRecorder(levelAt(least)) with context { Sink: r } { with Annotate("k", text) { Log(levelAt(at), message, []Attr{}) } } let mut want = []Entry{} if Admits(levelAt(least), levelAt(at)) { want = want.Append(Entry{Level: levelAt(at), Message: message, Attrs: []Attr{Attr{Key: "k", Text: text}}}) } r.Entries() == want }`,
}

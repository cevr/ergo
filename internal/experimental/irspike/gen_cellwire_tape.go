package irspike

// cellTape is GenerateCellWire's Tape provider, as lower's tapeSource
// draws it (wiretext_property_test.go): an operation type whose laws are
// true by construction, so a lowering that applies them prints what one
// that does not prints, and a state the walk carries wrong shows.
const cellTape = `type Tape {
	..Writer,
	mut Written() string,
	mut raw(text string),
	mut mark(k string),
	mut echo(),

	law RawJoins(a string, b string) { raw(a); raw(b) } == { raw(a + b) },
	law MarkLast(a string, b string) { mark(a); mark(b) } == { mark(b) },
	law EchoMark(a string) { mark(a); echo() } == { mark(a); raw(a + "") },
}

type tape { last string, out string } implements Tape

func NewTape() Tape { tape{last: "", out: ""} }
func raw(t mut tape, s string) { t.out = t.out + s }
func mark(t mut tape, k string) { t.last = k }
func echo(t mut tape) { t.out = t.out + t.last }
func Written(t mut tape) string { t.out }
func empty(s string) bool { s == "" }
`

// cellTapeEvents are the drawn event bodies: each Writer event's name,
// its parameter and the bodies it may take ($ a tail: nothing, or a
// mark("") that leaves the state known).
var cellTapeEvents = []struct {
	name, param string
	bodies      []string
}{
	{"Null", "", []string{`t.raw("n")$`}},
	{"Number", "s string", []string{`t.raw(s)$`}},
	{"Bool", "b bool", []string{`t.raw("b")$`, `t.raw("\(b)")$`}},
	{"Int", "n int64", []string{`t.raw("\(n)")$`, `t.raw("i")$`, `t.echo(); t.raw("\(n)")$`}},
	{"Uint", "n uint64", []string{`t.raw("\(n)")$`}},
	{"Float", "f float64", []string{`t.raw("f")$`}},
	{"String", "s string", []string{`if empty(s) { t.raw("e") } else { t.raw(s) }`, `t.raw(s)$`, `t.echo(); t.raw(s)$`}},
	// Writer's StringBytes (main's GX12), drawn as lower's tapeSource draws it.
	{"StringBytes", "b []byte", []string{`t.raw("y")$`}},
	{"BeginList", "n int", []string{`t.raw("[")$`, `t.echo(); t.raw("[")$`}},
	{"EndList", "", []string{`t.raw("]")$`, `t.echo(); t.raw("]")$`}},
	{"BeginObject", "n int", []string{`t.echo(); t.raw("{")`, `t.echo(); t.raw("{")$`}},
	{"Tag", "name string", []string{`t.raw(name)$`, `t.raw("#")$`, `t.echo(); t.raw(name); t.mark(name)`}},
	// Key ends in mark(k): the state a field's codec call starts in is
	// its key's, so one record type entered at two keys asks for two
	// entry states.
	{"Key", "k string", []string{`t.raw(k); t.mark(k)`, `t.echo(); t.raw(k); t.mark(k)`}},
	{"EndObject", "", []string{`t.raw("}")$`, `t.echo(); t.raw("}")$`}},
}

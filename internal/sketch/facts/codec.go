package facts

import (
	"encoding/binary"
	"errors"
)

// Version is the format version of an encoded table. The check snapshot
// stores a package's table as a section of its own under this version
// (the snapshot format is the CS lane's; this is the section's body).
const Version = 6

// Encode writes t as bytes: uvarints and length-prefixed strings in the
// tables' own order, so equal tables give equal bytes.
func Encode(t *Table) []byte {
	e := enc{}
	e.u(Version)
	e.s(t.Path)
	e.u(uint64(len(t.Files)))
	for _, f := range t.Files {
		e.s(f)
	}
	e.u(uint64(len(t.Symbols)))
	for _, s := range t.Symbols {
		e.s(s.Handle)
		e.u(uint64(s.Kind))
		e.s(s.Name)
		e.b(s.Declared)
		e.u(uint64(s.File))
		e.u(uint64(s.Off))
		e.b(s.Exported)
	}
	e.u(uint64(len(t.Sites)))
	for _, s := range t.Sites {
		e.u(uint64(s.Sym))
		e.u(uint64(s.Off))
		e.u(uint64(s.Encl + 1)) // NoSym is 0
		e.u(uint64(s.File))
		e.u(uint64(s.Kind))
		e.u(uint64(s.Flag))
	}
	for _, edges := range [][]Edge{t.Variants, t.Calls, t.Ops, t.Needs, t.Brings, t.Discharges, t.Provided, t.Fails, t.Raises, t.Caught} {
		e.u(uint64(len(edges)))
		for _, x := range edges {
			e.u(uint64(x.From))
			e.u(uint64(x.To))
			e.u(uint64(x.Site + 1))
		}
	}
	e.u(uint64(len(t.Machines)))
	for _, m := range t.Machines {
		e.u(uint64(m.Sym))
		e.ss(m.States)
		e.ss(m.Signals)
		e.s(m.Initial)
		e.u(uint64(len(m.Arms)))
		for _, a := range m.Arms {
			e.place(a.At)
			e.ss(a.States)
			e.ss(a.Signals)
			e.b(a.Partial)
			e.u(uint64(len(a.Edges)))
			for _, x := range a.Edges {
				e.s(x.Decision)
				e.s(x.To)
				e.place(x.At)
			}
			e.u(uint64(len(a.Alts)))
			for _, x := range a.Alts {
				e.ss(x.States)
				e.ss(x.Signals)
				e.b(x.Partial)
			}
		}
		e.u(uint64(len(m.Timers)))
		for _, x := range m.Timers {
			e.s(x.State)
			e.s(x.Signal)
			e.place(x.At)
		}
		e.u(uint64(len(m.Work)))
		for _, p := range m.Work {
			e.place(p)
		}
		e.u(uint64(len(m.Exit)))
		for _, p := range m.Exit {
			e.place(p)
		}
		e.ss(m.Unsure)
		e.ss(m.Computed)
	}
	return e.buf
}

type enc struct{ buf []byte }

func (e *enc) u(v uint64) { e.buf = binary.AppendUvarint(e.buf, v) }
func (e *enc) s(v string) { e.u(uint64(len(v))); e.buf = append(e.buf, v...) }
func (e *enc) b(v bool) {
	if v {
		e.u(1)
	} else {
		e.u(0)
	}
}
func (e *enc) ss(v []string) {
	e.u(uint64(len(v)))
	for _, s := range v {
		e.s(s)
	}
}
func (e *enc) place(p Place) { e.s(p.What); e.s(p.File); e.u(uint64(p.Line)) }

// ErrFormat is a section that is not an encoded table of this version.
var ErrFormat = errors.New("facts: not a table of this version")

// Decode reads a table Encode wrote, and rebuilds its derived indexes
// (the starts, the position index, the handle index).
func Decode(data []byte) (*Table, error) {
	d := dec{buf: data}
	if d.u() != Version {
		return nil, ErrFormat
	}
	t := &Table{Path: d.s()}
	for range d.n() {
		t.Files = append(t.Files, d.s())
	}
	for range d.n() {
		t.Symbols = append(t.Symbols, Symbol{Handle: d.s(), Kind: Kind(d.u()), Name: d.s(), Declared: d.b(),
			File: uint16(d.u()), Off: uint32(d.u()), Exported: d.b()})
	}
	for range d.n() {
		t.Sites = append(t.Sites, Site{Sym: uint32(d.u()), Off: uint32(d.u()), Encl: uint32(d.u()) - 1,
			File: uint16(d.u()), Kind: SiteKind(d.u()), Flag: uint8(d.u())})
	}
	edges := func() []Edge {
		var out []Edge
		for range d.n() {
			out = append(out, Edge{From: uint32(d.u()), To: uint32(d.u()), Site: uint32(d.u()) - 1})
		}
		return out
	}
	t.Variants, t.Calls, t.Ops = edges(), edges(), edges()
	t.Needs, t.Brings, t.Discharges = edges(), edges(), edges()
	t.Provided, t.Fails, t.Raises, t.Caught = edges(), edges(), edges(), edges()
	for range d.n() {
		m := MachineGraph{Sym: uint32(d.u()), States: d.ss(), Signals: d.ss(), Initial: d.s()}
		for range d.n() {
			a := Arm{At: d.place(), States: d.ss(), Signals: d.ss(), Partial: d.b()}
			for range d.n() {
				a.Edges = append(a.Edges, Transition{Decision: d.s(), To: d.s(), At: d.place()})
			}
			for range d.n() {
				a.Alts = append(a.Alts, Alt{States: d.ss(), Signals: d.ss(), Partial: d.b()})
			}
			m.Arms = append(m.Arms, a)
		}
		for range d.n() {
			m.Timers = append(m.Timers, Timer{State: d.s(), Signal: d.s(), At: d.place()})
		}
		for range d.n() {
			m.Work = append(m.Work, d.place())
		}
		for range d.n() {
			m.Exit = append(m.Exit, d.place())
		}
		m.Unsure = d.ss()
		m.Computed = d.ss()
		t.Machines = append(t.Machines, m)
	}
	if d.bad || len(d.buf) != 0 {
		return nil, ErrFormat
	}
	for _, s := range t.Sites {
		if int(s.Sym) >= len(t.Symbols) {
			return nil, ErrFormat
		}
	}
	reindex(t)
	return t, nil
}

type dec struct {
	buf []byte
	bad bool
}

func (d *dec) u() uint64 {
	v, n := binary.Uvarint(d.buf)
	if n <= 0 {
		d.bad, d.buf = true, nil
		return 0
	}
	d.buf = d.buf[n:]
	return v
}

// n is a count, bounded by the bytes left so a bad section cannot ask
// for more rows than it holds.
func (d *dec) n() int {
	v := d.u()
	if v > uint64(len(d.buf)) {
		d.bad, d.buf = true, nil
		return 0
	}
	return int(v)
}

func (d *dec) s() string {
	n := d.n()
	v := string(d.buf[:n])
	d.buf = d.buf[n:]
	return v
}

func (d *dec) b() bool { return d.u() != 0 }

func (d *dec) ss() []string {
	var out []string
	for range d.n() {
		out = append(out, d.s())
	}
	return out
}

func (d *dec) place() Place { return Place{What: d.s(), File: d.s(), Line: int(d.u())} }

// Command vt is DOD0 row 4: the step of a VT escape-sequence parser (the
// ground, escape and CSI states of Paul Williams' DEC parser, with OSC
// strings, as Ghostty's Parser.zig has them) as a switch (side switch)
// against a table indexed by state and byte (side table). The table is
// not written by hand: it is the switch, evaluated over every state and
// every byte when the program starts, as a table lowering of a machine
// would be (MT1). Both sides run the same actions and print the same
// counts.
//
//	go build -ldflags=-X=main.side=table -o vt-table ./vt
//	vt-table MIB ROUNDS
package main

import (
	"fmt"
	"os"
	"strconv"

	"ergo.local/receipts/dod0/internal/report"
)

// side is the step's form, set by the build: "switch" or "table".
var side = "switch"

type state uint8

const (
	ground state = iota
	escape
	escInter
	csiEntry
	csiParam
	csiInter
	csiIgnore
	oscString
	nstates
)

type action uint8

const (
	none action = iota
	print
	execute
	collect
	param
	escDispatch
	csiDispatch
	oscPut
	oscEnd
	clear
)

// step is the parser's transition: the next state and the action for byte
// b in state s.
func step(s state, b byte) (state, action) {
	// Anywhere.
	switch b {
	case 0x18, 0x1a:
		return ground, execute
	case 0x1b:
		if s == oscString {
			return escape, oscEnd
		}
		return escape, clear
	}
	switch s {
	case ground:
		switch {
		case b < 0x20:
			return ground, execute
		case b == 0x7f:
			return ground, none
		default:
			return ground, print
		}
	case escape:
		switch {
		case b < 0x20:
			return escape, execute
		case b <= 0x2f:
			return escInter, collect
		case b == '[':
			return csiEntry, clear
		case b == ']':
			return oscString, clear
		case b <= 0x7e:
			return ground, escDispatch
		default:
			return escape, none
		}
	case escInter:
		switch {
		case b < 0x20:
			return escInter, execute
		case b <= 0x2f:
			return escInter, collect
		case b <= 0x7e:
			return ground, escDispatch
		default:
			return escInter, none
		}
	case csiEntry, csiParam:
		switch {
		case b < 0x20:
			return s, execute
		case b <= 0x2f:
			return csiInter, collect
		case b <= 0x3b:
			return csiParam, param
		case b <= 0x3f:
			if s == csiEntry {
				return csiParam, collect
			}
			return csiIgnore, none
		case b <= 0x7e:
			return ground, csiDispatch
		default:
			return s, none
		}
	case csiInter:
		switch {
		case b < 0x20:
			return csiInter, execute
		case b <= 0x2f:
			return csiInter, collect
		case b <= 0x3f:
			return csiIgnore, none
		case b <= 0x7e:
			return ground, csiDispatch
		default:
			return csiInter, none
		}
	case csiIgnore:
		switch {
		case b < 0x20:
			return csiIgnore, execute
		case b >= 0x40 && b <= 0x7e:
			return ground, none
		default:
			return csiIgnore, none
		}
	case oscString:
		switch {
		case b == 0x07:
			return ground, oscEnd
		case b < 0x20:
			return oscString, none
		default:
			return oscString, oscPut
		}
	}
	return ground, none
}

// table is step over every state and byte: next state in the high byte,
// action in the low byte.
var table [nstates][256]uint16

func init() {
	for s := range nstates {
		for b := range 256 {
			n, a := step(s, byte(b))
			table[s][b] = uint16(n)<<8 | uint16(a)
		}
	}
}

// counts is what the parser did: each action's effect, summed.
type counts struct {
	printed, executed, collected, escs, csis, oscs, oscBytes int64
	params, paramSum                                         int64
	cur                                                      int64
}

func (c *counts) do(a action, b byte) {
	switch a {
	case print:
		c.printed++
	case execute:
		c.executed++
	case collect:
		c.collected++
	case param:
		if b == ';' || b == ':' {
			c.params++
			c.paramSum += c.cur
			c.cur = 0
		} else {
			c.cur = c.cur*10 + int64(b-'0')
		}
	case escDispatch:
		c.escs++
	case csiDispatch:
		c.csis++
		c.paramSum += c.cur
		c.cur = 0
	case oscPut:
		c.oscBytes++
	case oscEnd:
		c.oscs++
	case clear:
		c.cur = 0
	}
}

func parseSwitch(in []byte, c *counts) {
	s := ground
	for _, b := range in {
		var a action
		s, a = step(s, b)
		c.do(a, b)
	}
}

func parseTable(in []byte, c *counts) {
	s := ground
	for _, b := range in {
		e := table[s][b]
		s = state(e >> 8)
		c.do(action(e), b)
	}
}

// input is n bytes of terminal output: text lines with SGR and cursor
// sequences, some OSC titles, control bytes and stray bytes.
func input(n int) []byte {
	r := report.New(4)
	out := make([]byte, 0, n+64)
	for len(out) < n {
		switch k := r.IntN(100); {
		case k < 60:
			for range 1 + r.IntN(40) {
				out = append(out, byte(0x20+r.IntN(0x5f)))
			}
		case k < 80:
			out = append(out, 0x1b, '[')
			for i := range r.IntN(4) {
				if i > 0 {
					out = append(out, ';')
				}
				out = strconv.AppendInt(out, int64(r.IntN(256)), 10)
			}
			out = append(out, "mHJKABCDsu"[r.IntN(10)])
		case k < 85:
			out = append(out, 0x1b, ']', '0', ';')
			for range r.IntN(20) {
				out = append(out, byte('a'+r.IntN(26)))
			}
			out = append(out, 0x07)
		case k < 95:
			out = append(out, "\r\n\t\b"[r.IntN(4)])
		default:
			out = append(out, byte(r.IntN(256)))
		}
	}
	return out
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: vt MIB ROUNDS")
		os.Exit(2)
	}
	mib, err1 := strconv.Atoi(os.Args[1])
	rounds, err2 := strconv.Atoi(os.Args[2])
	if err1 != nil || err2 != nil || mib < 1 || mib > 1024 {
		fmt.Fprintln(os.Stderr, "vt: bad MIB or ROUNDS")
		os.Exit(2)
	}
	in := input(mib << 20)
	var c counts
	for range rounds {
		switch side {
		case "switch":
			parseSwitch(in, &c)
		case "table":
			parseTable(in, &c)
		default:
			fmt.Fprintln(os.Stderr, "vt: side is", side)
			os.Exit(2)
		}
	}
	fmt.Printf("%+v\n", c)
	report.Done(nil)
}

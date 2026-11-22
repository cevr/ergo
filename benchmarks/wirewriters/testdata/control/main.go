// Command control is writerbench's hand Go twin (W1), run as
// `control bench KIND N`: a record, or a list of four, written N times by
// a hand encoder on a Go Writer interface, whose writer adds up the size
// each event would take as JSON text, as writerbench's sizer does.
package main

import (
	"fmt"
	"os"
	"strconv"
)

type box struct {
	Label  string
	Count  int
	Tags   []string
	Weight float64
}

var one = box{Label: "crate", Count: 12, Tags: []string{"wood", "heavy"}, Weight: 30.5}

var four = []box{one, one, one, one}

// Writer is the prelude Writer's operations as a Go interface.
type Writer interface {
	Null()
	Number(text string)
	Bool(b bool)
	Int(n int64)
	Uint(n uint64)
	Float(f float64)
	String(s string)
	StringBytes(b []byte)
	BeginList(n int)
	EndList()
	BeginObject(n int)
	Tag(name string)
	Key(k string)
	EndObject()
}

type sizer struct{ n int }

func (s *sizer) Null()           { s.n += 4 }
func (s *sizer) Number(t string) { s.n += len(t) }
func (s *sizer) Bool(b bool) {
	if b {
		s.n += 4
	} else {
		s.n += 5
	}
}
func (s *sizer) Int(int64)            { s.n += 8 }
func (s *sizer) Uint(uint64)          { s.n += 8 }
func (s *sizer) Float(float64)        { s.n += 8 }
func (s *sizer) String(x string)      { s.n += len(x) + 2 }
func (s *sizer) StringBytes(b []byte) { s.n += len(b) + 2 }
func (s *sizer) BeginList(int)        { s.n++ }
func (s *sizer) EndList()             { s.n++ }
func (s *sizer) BeginObject(int)      { s.n++ }
func (s *sizer) Tag(name string)      { s.n += len(name) }
func (s *sizer) Key(k string)         { s.n += len(k) + 3 }
func (s *sizer) EndObject()           { s.n++ }

//go:noinline
func putBox(w Writer, b box) {
	w.BeginObject(4)
	w.Key("label")
	w.String(b.Label)
	w.Key("count")
	w.Int(int64(b.Count))
	w.Key("tags")
	w.BeginList(len(b.Tags))
	for _, t := range b.Tags {
		w.String(t)
	}
	w.EndList()
	w.Key("weight")
	w.Float(b.Weight)
	w.EndObject()
}

//go:noinline
func putList(w Writer, bs []box) {
	w.BeginList(len(bs))
	for _, b := range bs {
		putBox(w, b)
	}
	w.EndList()
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || (os.Args[2] != "record" && os.Args[2] != "list") {
		fmt.Fprintln(os.Stderr, "usage: control bench record|list N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	s := &sizer{}
	for i := 0; i < n; i++ {
		if os.Args[2] == "list" {
			putList(s, four)
			continue
		}
		putBox(s, one)
	}
	fmt.Println(s.n)
}

package rttrace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// TestTraceRecorderIsBounded checks the bound and the drop policy: the
// recorder keeps its first Cap events, counts every later one as dropped, and
// never blocks an emitting task, even with many goroutines past the bound.
func TestTraceRecorderIsBounded(t *testing.T) {
	const capacity, writers, each = 8, 16, 1000
	rec := New(capacity)
	root := rt.TracedRoot("app.main", rec)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				root.Trace(rt.TraceConstruct, "app.Live", "app.sketch:1:1")
			}
		}()
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-testwait.After(t):
		t.Fatal("emitting tasks blocked on a full recorder")
	}
	recs, inFlight := rec.Records()
	if len(recs) != capacity || inFlight != 0 || rec.Dropped() != writers*each-capacity {
		t.Fatalf("kept %d, in flight %d, dropped %d; want %d kept, 0 in flight, %d dropped",
			len(recs), inFlight, rec.Dropped(), capacity, writers*each-capacity)
	}
	for i, r := range recs {
		if r.Seq != uint64(i) {
			t.Fatalf("record %d has seq %d; want the first events kept in arrival order", i, r.Seq)
		}
	}
	rec.Reset()
	if recs, _ := rec.Records(); len(recs) != 0 || rec.Dropped() != 0 {
		t.Fatalf("after Reset: %d kept, %d dropped; want none", len(recs), rec.Dropped())
	}
	if none := New(0); true {
		none.Event(rt.Event{Kind: rt.TraceCall})
		if recs, _ := none.Records(); len(recs) != 0 || none.Dropped() != 1 {
			t.Fatalf("a zero-capacity recorder kept %d and dropped %d; want 0 and 1", len(recs), none.Dropped())
		}
	}
}

// The hand encoder writes what encoding/json reads back unchanged, including
// quotes, control characters, non-ASCII text and invalid UTF-8.
func TestEncodeRoundTripsThroughEncodingJSON(t *testing.T) {
	want := Line{Seq: 7, Time: -3, ID: 9, Link: 4, Kind: "body-exit", Task: 2, Parent: 1, Def: "a\"b\\c\n\x01é→", Site: "s.sketch:1:2",
		Cause: []Reason{{Reason: "defect", Name: "x\x7f", InRelease: true, Chain: []Frame{{Task: 2, Def: "d"}, {Task: 1, Def: "e"}}}, {Reason: "interrupted", Origin: 5}}}
	var got Line
	if err := json.Unmarshal(want.appendJSON(nil), &got); err != nil {
		t.Fatal(err)
	}
	if got.Def != want.Def || got.Seq != 7 || got.Time != -3 || got.ID != 9 || got.Link != 4 || len(got.Cause) != 2 || !got.Cause[0].InRelease || len(got.Cause[0].Chain) != 2 || got.Cause[0].Name != "x\x7f" || got.Cause[1].Origin != 5 || got.Cause[0].Origin != 0 {
		t.Fatalf("round trip: %+v; want %+v", got, want)
	}
	var bad Line
	if err := json.Unmarshal(Line{Def: "\xff"}.appendJSON(nil), &bad); err != nil || bad.Def != "�" {
		t.Fatalf("invalid UTF-8: %q, %v; want U+FFFD", bad.Def, err)
	}
	var h Header
	if err := json.Unmarshal(Header{Schema: Schema, Revision: "r", InFlight: 2, Dropped: 3}.appendJSON(nil), &h); err != nil || h.InFlight != 2 || h.Dropped != 3 {
		t.Fatalf("header round trip: %+v, %v", h, err)
	}
}

type orderNotFound struct{ ID string }

func (orderNotFound) Error() string { return "OrderNotFound" }

// The JSON Lines trace has one header with the revision, then one line per
// event with its kind, task, parent from the start event, definition ID, site
// (its own, or its definition's) and a cause rendered by type name only.
func TestWriteJSONLNamesTypesNotValues(t *testing.T) {
	recs := []Record{
		{Seq: 0, Event: rt.Event{Kind: rt.TraceStart, Task: 2, Parent: 1, Def: "app.f/timeout.After#1"}},
		{Seq: 1, Event: rt.Event{Kind: rt.TraceCall, Task: 2, Parent: 1, Def: "app.Orders.Find", Site: "app.sketch:7:3"}},
		{Seq: 2, Event: rt.Event{Kind: rt.TraceBodyExit, Task: 2, Parent: 0, Def: "app.f/timeout.After#1", Cause: orderNotFound{ID: "secret-7"}}},
		{Seq: 3, Event: rt.Event{Kind: rt.TraceBodyExit, Task: 3, Def: "app.g", Cause: rt.Expired{}}},
		{Seq: 4, Event: rt.Event{Kind: rt.TraceCallExit, ID: 8, Link: 5, Task: 2, Def: "app.Orders.Find", Site: "app.sketch:7:3", Cause: orderNotFound{ID: "secret-8"}}},
		{Seq: 5, Event: rt.Event{Kind: rt.TraceCallExit, ID: 9, Link: 6, Task: 2, Def: "app.Orders.Find", Site: "app.sketch:7:3", Defect: "secret-9"}},
		{Seq: 6, Event: rt.Event{Kind: rt.TraceCallExit, ID: 10, Link: 7, Task: 2, Def: "app.Orders.Find", Site: "app.sketch:7:3"}},
	}
	var buf bytes.Buffer
	sites := map[string]string{"app.f/timeout.After#1": "app.sketch:6:12"}
	if err := WriteJSONL(&buf, Header{Revision: "abc", Producer: "test"}, recs, sites); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "secret-7") {
		t.Fatalf("trace leaks a field value:\n%s", buf.String())
	}
	sc := bufio.NewScanner(&buf)
	sc.Scan()
	var h Header
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil || h.Schema != Schema || h.Revision != "abc" || h.Events != 7 {
		t.Fatalf("header %+v (%v); want schema %s, revision abc, 7 events", h, err, Schema)
	}
	var lines []Line
	for sc.Scan() {
		var l Line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, l)
	}
	if len(lines) != 7 {
		t.Fatalf("%d event lines; want 7", len(lines))
	}
	if l := lines[0]; l.Kind != "start" || l.Site != "app.sketch:6:12" || l.Parent != 1 {
		t.Errorf("start line %+v; want its definition's site and parent 1", l)
	}
	if l := lines[1]; l.Kind != "call" || l.Site != "app.sketch:7:3" {
		t.Errorf("call line %+v; want its own site", l)
	}
	if l := lines[2]; l.Parent != 1 || len(l.Cause) != 1 || l.Cause[0].Name != "orderNotFound" {
		t.Errorf("exit line %+v; want parent 1 from the start event and cause orderNotFound", l)
	}
	if l := lines[3]; len(l.Cause) != 1 || l.Cause[0].Name != "timeout.Expired" {
		t.Errorf("exit line %+v; want cause timeout.Expired", l)
	}
	if l := lines[4]; l.Kind != "call-exit" || l.ID != 8 || l.Link != 5 || len(l.Cause) != 1 || l.Cause[0].Reason != "failure" || l.Cause[0].Name != "orderNotFound" {
		t.Errorf("failed call-exit line %+v; want id 8, link 5, failure orderNotFound", l)
	}
	if l := lines[5]; len(l.Cause) != 1 || l.Cause[0].Reason != "defect" || l.Cause[0].Name != "string" {
		t.Errorf("defect call-exit line %+v; want defect string", l)
	}
	if l := lines[6]; l.Link != 7 || len(l.Cause) != 0 {
		t.Errorf("ok call-exit line %+v; want link 7 and no cause", l)
	}
}

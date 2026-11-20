package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdKvLawsProbe restates the laws whose runner is TestStdKvLawsRun
// as Ergo tests. A member law runs on a provider of each kind the probe
// draws; a law over a package's private helpers restates them.
const stdKvLawsProbe = `import fs
import fstest
import json
import kv
import kv/file
import kv/memory
import strings

// provider is a store of each kind, by pick: a memory store, or a file
// store over its own fstest.Memory disk.
func provider(pick int) kv.Store {
	if ((pick%2)+2)%2 == 0 {
		return memory.New()
	}
	let disk = fstest.NewMemory()
	file.Open(disk, disk, "kv.log") catch {
	case kv.Failed:
		Unreachable("an empty disk opens")
	}
}

func getAfterPut(s kv.Store, key string, first string, second string) bool fails kv.Failed {
	try s.Put(key, first)
	let one = try s.Get(key)
	try s.Put(key, second)
	one == Some[string]{Value: first} && try s.Get(key) == Some[string]{Value: second}
}

test KvStoreGetAfterPut(pick int, key string, first string, second string) {
	let ok = getAfterPut(provider(pick), key, first, second) catch {
	case kv.Failed:
		false
	}
	assert ok
}

func putKeepsOthers(s kv.Store, key string, other string, a string, b string) bool fails kv.Failed {
	if key == other {
		return true
	}
	try s.Put(other, a)
	try s.Put(key, b)
	let kept = try s.Get(other)
	let _ = try s.Delete(key)
	kept == Some[string]{Value: a} && try s.Get(other) == Some[string]{Value: a}
}

test KvStorePutKeepsOthers(pick int, key string, other string, a string, b string) {
	let ok = putKeepsOthers(provider(pick), key, other, a, b) catch {
	case kv.Failed:
		false
	}
	assert ok
}

func deleteIsAbsent(s kv.Store, key string, value string) bool fails kv.Failed {
	try s.Put(key, value)
	let held = try s.Delete(key)
	if !held || try s.Get(key) != None {
		return false
	}
	let mut seen = false
	let sc = try s.Scan(key)
	sc(func(e kv.Entry) bool {
		if e.Key == key {
			seen = true
		}
		true
	})
	!seen && !try s.Delete(key)
}

test KvStoreDeleteIsAbsent(pick int, key string, value string) {
	let ok = deleteIsAbsent(provider(pick), key, value) catch {
	case kv.Failed:
		false
	}
	assert ok
}

func scanIsSorted(s kv.Store, keys []string, cut int) bool fails kv.Failed {
	for k in keys {
		try s.Put(k, "v " + k)
	}
	let first = if len(keys) == 0 { "" } else { keys[0] }
	let span = len(first) + 1
	let prefix = strings.Slice(first, 0, ((cut%span)+span)%span)
	let mut got = []kv.Entry{}
	let sc = try s.Scan(prefix)
	sc(func(e kv.Entry) bool {
		got = got.Append(e)
		true
	})
	for i, e in got {
		if !strings.HasPrefix(e.Key, prefix) || (i > 0 && got[i-1].Key >= e.Key) {
			return false
		}
		if try s.Get(e.Key) != (Some[string]{Value: e.Value}) {
			return false
		}
	}
	for k in keys {
		if strings.HasPrefix(k, prefix) {
			let mut found = false
			for e in got {
				if e.Key == k {
					found = true
				}
			}
			if !found {
				return false
			}
		}
	}
	true
}

test KvStoreScanIsSorted(pick int, keys []string, cut int) {
	let ok = scanIsSorted(provider(pick), keys, cut) catch {
	case kv.Failed:
		false
	}
	assert ok
}

func scanStops(s kv.Store, keys []string, prefix string, k int) bool fails kv.Failed {
	if k < 1 {
		return true
	}
	for key in keys {
		try s.Put(prefix + key, key)
	}
	let mut n = 0
	let all = try s.Scan(prefix)
	all(func(e kv.Entry) bool {
		n = n + 1
		true
	})
	let mut calls = 0
	let sc = try s.Scan(prefix)
	sc(func(e kv.Entry) bool {
		calls = calls + 1
		calls < k
	})
	calls == if k < n { k } else { n }
}

test KvStoreScanStops(pick int, keys []string, prefix string, k int) {
	let ok = scanStops(provider(pick), keys, prefix, k) catch {
	case kv.Failed:
		false
	}
	assert ok
}

// note is memory's law record, restated.
type note { Title string, Tags []string, N int }

test MemoryOfRoundTrips(key string, title string, tags []string, n int) {
	let s = memory.New()
	let notes = kv.Of[note](s)
	let x = note{Title: title, Tags: tags, N: n}
	let ok = func() bool fails kv.Failed | json.Malformed | WireMissingField | WireWrongKind {
		try notes.Put(key, x)
		if try notes.Get(key) != (Some[note]{Value: x}) || !try notes.Delete(key) || try notes.Get(key) != None {
			return false
		}
		try s.Put(key, "{")
		let _ = notes.Get(key) catch {
		case json.Malformed:
			return true
		case kv.Failed, WireMissingField, WireWrongKind:
			return false
		}
		false
	}() catch {
	case kv.Failed, json.Malformed, WireMissingField, WireWrongKind:
		false
	}
	assert ok
}

// step is file's law script step, restated.
type step = putStep{Key int, Value string} | deleteStep{Key int} | compactStep{} | reopenStep{}

func keyOf(n int) string { "k\(((n%4)+4)%4)" }

func entries(s kv.Store) []kv.Entry fails kv.Failed {
	let mut out = []kv.Entry{}
	let all = try s.Scan("")
	all(func(e kv.Entry) bool {
		out = out.Append(e)
		true
	})
	out
}

func fileIsMemory(steps []step, torn string) bool fails kv.Failed | fs.NotFound | fs.Denied | fs.Failed {
	let disk = fstest.NewMemory()
	let want = memory.New()
	let mut got = try file.Open(disk, disk, "kv.log")
	for st in steps {
		match st {
		case putStep as p:
			try want.Put(keyOf(p.Key), p.Value)
			try got.Put(keyOf(p.Key), p.Value)
		case deleteStep as d:
			if try want.Delete(keyOf(d.Key)) != try got.Delete(keyOf(d.Key)) {
				return false
			}
		case compactStep:
			try got.Compact()
		case reopenStep:
			got = try file.Open(disk, disk, "kv.log")
		}
	}
	if try entries(got) != try entries(want) {
		return false
	}
	try disk.AppendFile("kv.log", "put\t" + strings.ReplaceAll(torn, "\n", ""))
	let back = try file.Open(disk, disk, "kv.log")
	if try entries(back) != try entries(want) {
		return false
	}
	try back.Put("after", torn)
	try want.Put("after", torn)
	try entries(try file.Open(disk, disk, "kv.log")) == try entries(want)
}

test FileFileIsMemory(steps []step, torn string) {
	let ok = fileIsMemory(steps, torn) catch {
	case kv.Failed, fs.NotFound, fs.Denied, fs.Failed:
		false
	}
	assert ok
}

func openRefusesABrokenLine(key string, value string, line string) bool fails kv.Failed | fs.NotFound | fs.Denied | fs.Failed {
	let disk = fstest.NewMemory()
	let s = try file.Open(disk, disk, "kv.log")
	try s.Put(key, value)
	try disk.AppendFile("kv.log", "x" + strings.ReplaceAll(line, "\n", "") + "\n")
	let _ = file.Open(disk, disk, "kv.log") catch {
	case kv.Failed:
		return true
	}
	false
}

test FileOpenRefusesABrokenLine(key string, value string, line string) {
	let ok = openRefusesABrokenLine(key, value, line) catch {
	case kv.Failed, fs.NotFound, fs.Denied, fs.Failed:
		false
	}
	assert ok
}
`

// stdKvLawStatements is the statement of each law the probe restates,
// by ledger key; lawledger.Run holds it equal to the ledger.
var stdKvLawStatements = map[string]string{
	"kv Store.GetAfterPut":           `GetAfterPut(key string, first string, second string) { try Put(key, first) let one = try Get(key) try Put(key, second) one == Some[string]{Value: first} && try Get(key) == Some[string]{Value: second} }`,
	"kv Store.PutKeepsOthers":        `PutKeepsOthers(key string, other string, a string, b string) { if key == other { return true } try Put(other, a) try Put(key, b) let kept = try Get(other) let _ = try Delete(key) kept == Some[string]{Value: a} && try Get(other) == Some[string]{Value: a} }`,
	"kv Store.DeleteIsAbsent":        `DeleteIsAbsent(key string, value string) { try Put(key, value) let held = try Delete(key) if !held || try Get(key) != None { return false } let mut seen = false let s = try Scan(key) s(func(e Entry) bool { if e.Key == key { seen = true } true }) !seen && !try Delete(key) }`,
	"kv Store.ScanIsSorted":          `ScanIsSorted(keys []string, cut int) { for k in keys { try Put(k, "v " + k) } let first = if len(keys) == 0 { "" } else { keys[0] } let span = len(first) + 1 let prefix = strings.Slice(first, 0, ((cut%span)+span)%span) let mut got = []Entry{} let s = try Scan(prefix) s(func(e Entry) bool { got = got.Append(e) true }) for i, e in got { if !strings.HasPrefix(e.Key, prefix) || (i > 0 && got[i-1].Key >= e.Key) { return false } if try Get(e.Key) != (Some[string]{Value: e.Value}) { return false } } for k in keys { if strings.HasPrefix(k, prefix) { let mut found = false for e in got { if e.Key == k { found = true } } if !found { return false } } } true }`,
	"kv Store.ScanStops":             `ScanStops(keys []string, prefix string, k int) { if k < 1 { return true } for key in keys { try Put(prefix + key, key) } let mut n = 0 let all = try Scan(prefix) all(func(e Entry) bool { n = n + 1 true }) let mut calls = 0 let s = try Scan(prefix) s(func(e Entry) bool { calls = calls + 1 calls < k }) calls == if k < n { k } else { n } }`,
	"kv/memory OfRoundTrips":         `OfRoundTrips(key string, title string, tags []string, n int) { let s = New() let notes = kv.Of[note](s) let x = note{Title: title, Tags: tags, N: n} try notes.Put(key, x) if try notes.Get(key) != (Some[note]{Value: x}) || !try notes.Delete(key) || try notes.Get(key) != None { return false } try s.Put(key, "{") let _ = notes.Get(key) catch { case json.Malformed: return true case kv.Failed, WireMissingField, WireWrongKind: return false } false }`,
	"kv/file FileIsMemory":           `FileIsMemory(steps []step, torn string) { let disk = fstest.NewMemory() let want = memory.New() let mut got = try Open(disk, disk, "kv.log") for st in steps { match st { case putStep as p: try want.Put(keyOf(p.Key), p.Value) try got.Put(keyOf(p.Key), p.Value) case deleteStep as d: if try want.Delete(keyOf(d.Key)) != try got.Delete(keyOf(d.Key)) { return false } case compactStep: try got.Compact() case reopenStep: got = try Open(disk, disk, "kv.log") } } if try entries(got) != try entries(want) { return false } try disk.AppendFile("kv.log", "put\t" + strings.ReplaceAll(torn, "\n", "")) let back = try Open(disk, disk, "kv.log") if try entries(back) != try entries(want) { return false } try back.Put("after", torn) try want.Put("after", torn) try entries(try Open(disk, disk, "kv.log")) == try entries(want) }`,
	"kv/file OpenRefusesABrokenLine": `OpenRefusesABrokenLine(key string, value string, line string) { let disk = fstest.NewMemory() let s = try Open(disk, disk, "kv.log") try s.Put(key, value) try disk.AppendFile("kv.log", "x" + strings.ReplaceAll(line, "\n", "") + "\n") let _ = Open(disk, disk, "kv.log") catch { case kv.Failed: return true } false }`,
}

// stdKvLawTestName is the probe's test of a ledger key: the last
// element of the package path, capitalized, then the law without its dot
// ("ratelimit/memory AgreesWithTheModel" is MemoryAgreesWithTheModel).
func stdKvLawTestName(key string) string {
	pkg, law, _ := strings.Cut(key, " ")
	return stdLawsTestName(pkg[strings.LastIndex(pkg, "/")+1:], law)
}

// TestStdKvLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. It builds Go because the laws run in the built test binary (ergo test), the only place a law runs; the file store's laws write through fstest.Memory, which runs in that binary.
func TestStdKvLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdkv"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdkv failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, stdKvLawStatements, stdKvLawTestName, stdKvLawsProbe, stdout))
}

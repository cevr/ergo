package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdLLMLawsProbe restates the laws of std llm and llm/memory (wf10 L1) as
// Ergo property tests: the Model laws over memory.Scripted (drawn by its
// NewScripted generator), the Fold, SafetyOf and Valid laws over generated
// values, and memory's CutIsTruncated. The helpers are the package's own,
// restated over its exported API.
const stdLLMLawsProbe = `import llm
import llm/memory
import strings
import sync

type streamed = gotTurn{Turn llm.Turn, Deltas []llm.Delta} | gotFailure{Failure llm.Failure, Deltas []llm.Delta} | sinkFailed{}

func first(r llm.Request) llm.Request {
	llm.Request{System: r.System, Messages: []llm.Message{}, Tools: r.Tools, Choice: r.Choice, Format: r.Format, MaxTokens: r.MaxTokens}
}

func stream(r llm.Request) streamed needs llm.Model {
	let got = sync.NewRef[[]llm.Delta]([]llm.Delta{})
	let t = llm.Model.Stream(r, func(d llm.Delta) fails llm.SinkFailed { got.Update(func(ds []llm.Delta) []llm.Delta { ds.Append(d) }) }) catch {
	case llm.SinkFailed:
		return sinkFailed
	case f:
		let failure llm.Failure = f
		return gotFailure{Failure: failure, Deltas: got.Get()}
	}
	gotTurn{Turn: t, Deltas: got.Get()}
}

func generate(r llm.Request) streamed needs llm.Model {
	let t = llm.Model.Generate(r) catch {
	case f:
		let failure llm.Failure = f
		return gotFailure{Failure: failure, Deltas: []llm.Delta{}}
	}
	gotTurn{Turn: t, Deltas: []llm.Delta{}}
}

func foldsToGenerate(r llm.Request) bool needs llm.Model {
	match (stream(r), generate(r)) {
	case (gotTurn as s, gotTurn as g):
		s.Turn == g.Turn
	case (gotFailure as s, gotFailure as g):
		s.Failure == g.Failure
	case (_, _):
		false
	}
}

func returnsItsFold(r llm.Request) bool needs llm.Model {
	match stream(r) {
	case gotTurn as s:
		let d = llm.Fold(s.Deltas)
		d.Text == s.Turn.Text && d.Calls == s.Turn.Calls
	case gotFailure:
		true
	case sinkFailed:
		false
	}
}

func notATurn(r llm.Request) bool needs llm.Model {
	match stream(r) {
	case gotTurn as s:
		len(llm.Fold(s.Deltas).Open) == 0
	case gotFailure as s:
		match s.Failure {
		case llm.Truncated as t:
			t.Text == llm.Fold(s.Deltas).Text
		case llm.RateLimited, llm.Overloaded, llm.Unauthorized, llm.Refused, llm.Transport, llm.InvalidRequest, llm.InvalidResponse, llm.UnsupportedSchema:
			true
		}
	case sinkFailed:
		false
	}
}

func distinct(calls []llm.Call) bool {
	for i, a in calls {
		for b in calls[i+1:] {
			if a.Id == b.Id {
				return false
			}
		}
	}
	true
}

func whole(r llm.Request) bool needs llm.Model {
	match stream(r) {
	case gotTurn as s:
		distinct(s.Turn.Calls) && llm.Fold(s.Deltas).Calls == s.Turn.Calls
	case gotFailure:
		true
	case sinkFailed:
		false
	}
}

test LlmModelStreamFoldsToGenerate(head []string, tools []string, cut bool, rest []memory.Reply, r llm.Request) {
	let s = memory.NewScripted(head, tools, cut, rest)
	assert with context { llm.Model: s } { foldsToGenerate(r) && foldsToGenerate(first(r)) }
}

test LlmModelStreamReturnsItsFold(head []string, tools []string, cut bool, rest []memory.Reply, r llm.Request) {
	let s = memory.NewScripted(head, tools, cut, rest)
	assert with context { llm.Model: s } { returnsItsFold(r) && returnsItsFold(first(r)) }
}

test LlmModelTruncatedIsNotATurn(head []string, tools []string, cut bool, rest []memory.Reply, r llm.Request) {
	let s = memory.NewScripted(head, tools, cut, rest)
	assert with context { llm.Model: s } { notATurn(r) && notATurn(first(r)) }
}

test LlmModelCallsAreWhole(head []string, tools []string, cut bool, rest []memory.Reply, r llm.Request) {
	let s = memory.NewScripted(head, tools, cut, rest)
	assert with context { llm.Model: s } { whole(r) && whole(first(r)) }
}

func pieces(text string, cuts []int) []string {
	let n = len(text)
	let mut out = []string{}
	let mut at = 0
	for c in cuts {
		let to = ((c % (n + 1)) + n + 1) % (n + 1)
		if to > at {
			out = out.Append(strings.Slice(text, at, to))
			at = to
		}
	}
	out.Append(strings.Slice(text, at, n))
}

func chunked(ps []string) []llm.Delta {
	let mut ds = []llm.Delta{llm.CallStart{Index: 0, Id: "c", Name: "n"}}
	for p in ps {
		ds = ds.Append(llm.Text{Text: p}).Append(llm.Reasoning{Text: p}).Append(llm.CallArgs{Index: 0, Text: p})
	}
	ds.Append(llm.CallEnd{Index: 0})
}

test LlmFoldIsChunkFree(text string, cutsA []int, cutsB []int) {
	let a = pieces(text, cutsA)
	let b = pieces(text, cutsB)
	assert llm.Fold(chunked(a)) == llm.Fold(chunked(b)) && llm.Fold(chunked(a)).Text == text && llm.Fold(chunked(a)).Calls[0].Arguments == text
}

test LlmFoldKeepsCallsApart(a string, b string, cuts []int) {
	let pa = pieces(a, cuts)
	let pb = pieces(b, cuts)
	let mut ds = []llm.Delta{llm.CallStart{Index: 7, Id: "a", Name: "x"}, llm.CallStart{Index: 3, Id: "b", Name: "y"}}
	let n = if len(pa) > len(pb) { len(pa) } else { len(pb) }
	for i in 0..n {
		if i < len(pb) {
			ds = ds.Append(llm.CallArgs{Index: 3, Text: pb[i]})
		}
		if i < len(pa) {
			ds = ds.Append(llm.CallArgs{Index: 7, Text: pa[i]})
		}
	}
	let d = llm.Fold(ds.Append(llm.CallEnd{Index: 3}).Append(llm.CallEnd{Index: 7}))
	assert d.Calls == []llm.Call{llm.Call{Id: "a", Name: "x", Arguments: a}, llm.Call{Id: "b", Name: "y", Arguments: b}} && len(d.Open) == 0
}

test LlmRequestFaultsAreUnsafe(f llm.Failure) {
	match f {
	case llm.Unauthorized, llm.Refused, llm.InvalidRequest, llm.UnsupportedSchema:
		assert llm.SafetyOf(f) == llm.Unsafe
	case llm.RateLimited, llm.Overloaded:
		assert llm.SafetyOf(f) == llm.Safe
	case llm.Transport, llm.Truncated, llm.InvalidResponse:
		assert llm.SafetyOf(f) == llm.Unknown
	}
}

test LlmAnsweredCallsAreValid(text string, names []string) {
	let mut calls = []llm.Call{}
	let mut results = []llm.Message{}
	for i, n in names {
		calls = calls.Append(llm.Call{Id: "call-\(i)", Name: n, Arguments: "{}"})
		results = results.Append(llm.Result{CallId: "call-\(i)", Text: n, Failed: false})
	}
	let asked = []llm.Message{llm.User{Text: text}, llm.Assistant{Text: text, Calls: calls}}
	assert llm.Valid(asked.Concat(results).Append(llm.User{Text: text}))
	if len(names) > 0 {
		let early = []llm.Message{llm.User{Text: text}, results[0], llm.Assistant{Text: text, Calls: calls}}.Concat(results[1:])
		let missing = asked.Concat(results[1:])
		let twice = asked.Concat(results).Append(results[0])
		assert !llm.Valid(early) && !llm.Valid(missing) && !llm.Valid(twice)
	}
}

func cutIsTruncated(first []string, tools []string) bool {
	let s = memory.NewScripted(first, tools, true, []memory.Reply{})
	let r = llm.Request{System: "", Messages: []llm.Message{}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0}
	let want = llm.Truncated{Text: strings.Join(first, "")}
	let _ = memory.Generate(s, r) catch {
	case llm.Truncated as t:
		let _ = memory.Stream(s, r, func(d llm.Delta) fails llm.SinkFailed {}) catch {
		case llm.Truncated as u:
			return t == want && u == want
		case _:
			return false
		}
		return false
	case _:
		return false
	}
	false
}

test MemoryCutIsTruncated(first []string, tools []string) { assert cutIsTruncated(first, tools) }
`

// stdLLMLawStatements is the statement of each llm law, as the ledger
// writes it.
var stdLLMLawStatements = map[string]string{
	"llm Model.StreamFoldsToGenerate": `StreamFoldsToGenerate(r Request) { foldsToGenerate(r) && foldsToGenerate(first(r)) }`,
	"llm Model.StreamReturnsItsFold":  `StreamReturnsItsFold(r Request) { returnsItsFold(r) && returnsItsFold(first(r)) }`,
	"llm Model.TruncatedIsNotATurn":   `TruncatedIsNotATurn(r Request) { notATurn(r) && notATurn(first(r)) }`,
	"llm Model.CallsAreWhole":         `CallsAreWhole(r Request) { whole(r) && whole(first(r)) }`,
	"llm FoldIsChunkFree":             `FoldIsChunkFree(text string, cutsA []int, cutsB []int) { let a = pieces(text, cutsA) let b = pieces(text, cutsB) Fold(chunked(a)) == Fold(chunked(b)) && Fold(chunked(a)).Text == text && Fold(chunked(a)).Calls[0].Arguments == text }`,
	"llm FoldKeepsCallsApart":         `FoldKeepsCallsApart(a string, b string, cuts []int) { let pa = pieces(a, cuts) let pb = pieces(b, cuts) let mut ds = []Delta{CallStart{Index: 7, Id: "a", Name: "x"}, CallStart{Index: 3, Id: "b", Name: "y"}} let n = if len(pa) > len(pb) { len(pa) } else { len(pb) } for i in 0..n { if i < len(pb) { ds = ds.Append(CallArgs{Index: 3, Text: pb[i]}) } if i < len(pa) { ds = ds.Append(CallArgs{Index: 7, Text: pa[i]}) } } let d = Fold(ds.Append(CallEnd{Index: 3}).Append(CallEnd{Index: 7})) d.Calls == []Call{Call{Id: "a", Name: "x", Arguments: a}, Call{Id: "b", Name: "y", Arguments: b}} && len(d.Open) == 0 }`,
	"llm RequestFaultsAreUnsafe":      `RequestFaultsAreUnsafe(f Failure) { match f { case Unauthorized, Refused, InvalidRequest, UnsupportedSchema: SafetyOf(f) == Unsafe case RateLimited, Overloaded: SafetyOf(f) == Safe case Transport, Truncated, InvalidResponse: SafetyOf(f) == Unknown } }`,
	"llm AnsweredCallsAreValid":       `AnsweredCallsAreValid(text string, names []string) { let mut calls = []Call{} let mut results = []Message{} for i, n in names { calls = calls.Append(Call{Id: "call-\(i)", Name: n, Arguments: "{}"}) results = results.Append(Result{CallId: "call-\(i)", Text: n, Failed: false}) } let asked = []Message{User{Text: text}, Assistant{Text: text, Calls: calls}} if !Valid(asked.Concat(results).Append(User{Text: text})) { return false } if len(names) == 0 { return true } let early = []Message{User{Text: text}, results[0], Assistant{Text: text, Calls: calls}}.Concat(results[1:]) let missing = asked.Concat(results[1:]) let twice = asked.Concat(results).Append(results[0]) !Valid(early) && !Valid(missing) && !Valid(twice) }`,
	"llm/memory CutIsTruncated":       `CutIsTruncated(first []string, tools []string) { let s = NewScripted(first, tools, true, []Reply{}) let r = llm.Request{System: "", Messages: []llm.Message{}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0} let want = llm.Truncated{Text: strings.Join(first, "")} let _ = Generate(s, r) catch { case llm.Truncated as t: let _ = Stream(s, r, func(d llm.Delta) fails llm.SinkFailed {}) catch { case llm.Truncated as u: return t == want && u == want case _: return false } return false case _: return false } false }`,
}

// stdLLMLawTestName is the probe's test of a ledger key: the last element
// of the package path, capitalized, then the law ("llm/memory
// CutIsTruncated" is MemoryCutIsTruncated).
func stdLLMLawTestName(key string) string {
	pkg, law, _ := strings.Cut(key, " ")
	return stdLawsTestName(pkg[strings.LastIndex(pkg, "/")+1:], law)
}

// TestStdLLMLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's llm laws, each with
// the row's statement, and that each passed. The llm-* mutants of the std
// mutant ledger name it.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs on the lowered runtime.
func TestStdLLMLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdllm"), "--seed", "1")
	if code != 0 {
		t.Errorf("the llm law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, stdLLMLawStatements, stdLLMLawTestName, stdLLMLawsProbe, stdout))
}

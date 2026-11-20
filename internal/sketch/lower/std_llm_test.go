package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdLLMLawsProbe restates the laws of std llm and llm/memory (wf10 L1, L2)
// as Ergo property tests: the Model laws over memory.Scripted (drawn by its
// NewScripted generator) and over memory.Sequence, the policy laws of
// llm.Retry, llm.Fallback and llm.Metered over memory.Sequence, the laws
// of llm/recorded (wf10 L3) over its exported API, the Fold, SafetyOf and Valid laws over generated
// values, and memory's CutIsTruncated. The helpers are the package's own,
// restated over its exported API.
const stdLLMLawsProbe = `import json
import jsonschema
import llm
import llm/memory
import llm/recorded
import schedule
import strings
import sync
import task
import testing

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
	let q = memory.NewSequence(rest)
	assert with context { llm.Model: s } { foldsToGenerate(r) && foldsToGenerate(first(r)) }
	assert with context { llm.Model: q } { foldsToGenerate(r) && foldsToGenerate(first(r)) }
}

test LlmModelStreamReturnsItsFold(head []string, tools []string, cut bool, rest []memory.Reply, r llm.Request) {
	let s = memory.NewScripted(head, tools, cut, rest)
	let q = memory.NewSequence(rest)
	assert with context { llm.Model: s } { returnsItsFold(r) && returnsItsFold(first(r)) }
	assert with context { llm.Model: q } { returnsItsFold(r) && returnsItsFold(first(r)) }
}

test LlmModelTruncatedIsNotATurn(head []string, tools []string, cut bool, rest []memory.Reply, r llm.Request) {
	let s = memory.NewScripted(head, tools, cut, rest)
	let q = memory.NewSequence(rest)
	assert with context { llm.Model: s } { notATurn(r) && notATurn(first(r)) }
	assert with context { llm.Model: q } { notATurn(r) && notATurn(first(r)) }
}

test LlmModelCallsAreWhole(head []string, tools []string, cut bool, rest []memory.Reply, r llm.Request) {
	let s = memory.NewScripted(head, tools, cut, rest)
	let q = memory.NewSequence(rest)
	assert with context { llm.Model: s } { whole(r) && whole(first(r)) }
	assert with context { llm.Model: q } { whole(r) && whole(first(r)) }
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
	let _ = s.Generate(r) catch {
	case llm.Truncated as t:
		let _ = s.Stream(r, func(d llm.Delta) fails llm.SinkFailed {}) catch {
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

func plain(text string) llm.Request {
	llm.Request{System: "", Messages: []llm.Message{llm.User{Text: text}}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0}
}

func ok(text string, u llm.Usage) memory.Reply { memory.Says{Deltas: []llm.Delta{llm.Text{Text: text}}, Stop: llm.Ended, Usage: u} }

func none() llm.Usage { llm.Usage{Input: 0, Output: 0, CacheRead: 0, CacheWrite: 0} }

func bare(f llm.Failure) llm.Failure {
	match f {
	case llm.Truncated:
		llm.Truncated{Text: ""}
	case llm.RateLimited, llm.Overloaded, llm.Unauthorized, llm.Refused, llm.Transport, llm.InvalidRequest, llm.InvalidResponse, llm.UnsupportedSchema:
		f
	}
}

func askedOnce(seqs []memory.Sequence) bool {
	for s in seqs {
		if memory.Asks(s) != 1 {
			return false
		}
	}
	true
}

func retryFollowsSafety(fs []llm.Failure, n int) bool {
	let budget = (n % 8 + 8) % 8
	let mut replies = []memory.Reply{}
	for f in fs {
		replies = replies.Append(memory.Breaks{Deltas: []llm.Delta{}, Failure: f})
	}
	let s = memory.NewSequence(replies.Append(ok("done", none())))
	let c = testing.NewVirtualClock(0)
	let mut stop = len(fs)
	for i, f in fs {
		if i < stop && (llm.SafetyOf(f) == llm.Unsafe || i == budget) {
			stop = i
		}
	}
	let t = llm.Retry{Inner: s, Schedule: schedule.Recurs(budget), Clock: c, Sleep: c}.Generate(plain("q")) catch {
	case f:
		let got llm.Failure = f
		return stop < len(fs) && got == bare(fs[stop]) && memory.Asks(s) == stop + 1
	}
	stop == len(fs) && t.Text == "done" && memory.Asks(s) == len(fs) + 1
}

test MemoryRetryFollowsSafety(fs []llm.Failure, n int) { assert retryFollowsSafety(fs, n) }

func retryHonoursAfter(ms int) bool {
	let wait = (ms % 100000 + 100000) % 100000
	let c = testing.NewVirtualClock(0)
	let s = memory.NewSequence([]memory.Reply{memory.Fails{Failure: llm.RateLimited{After: Some[int]{Value: wait}}}, ok("done", none())})
	let start = c.Now()
	let t = llm.Retry{Inner: s, Schedule: schedule.Spaced(1ms), Clock: c, Sleep: c}.Generate(plain("q")) catch {
	case _:
		return false
	}
	let waited = c.Now().Sub(start)
	t.Text == "done" && waited >= duration(int64(wait) * 1000000) && (wait > 0 || waited == 1ms)
}

test MemoryRetryHonoursAfter(ms int) { assert retryHonoursAfter(ms) }

func retryKeepsUpTo(n int) bool {
	let k = (n%5 + 5) % 5
	let mut replies = []memory.Reply{}
	for _ in 0..8 {
		let f llm.Failure = llm.Overloaded
		replies = replies.Append(memory.Fails{Failure: f})
	}
	let s = memory.NewSequence(replies.Append(ok("done", none())))
	let c = testing.NewVirtualClock(0)
	let limit = duration(int64(k)*1000000000 + 500000000)
	let _ = llm.Retry{Inner: s, Schedule: schedule.UpTo(schedule.Spaced(1s), limit), Clock: c, Sleep: c}.Generate(plain("q")) catch {
	case llm.Overloaded:
		return memory.Asks(s) == k+1
	case _:
		return false
	}
	false
}

test MemoryRetryKeepsUpTo(n int) { assert retryKeepsUpTo(n) }

func noReplayAfterFirstDelta(text string, f llm.Failure) bool {
	let r = plain("q")
	let failing = []memory.Reply{memory.Breaks{Deltas: []llm.Delta{llm.Text{Text: text}}, Failure: f}, ok("again", none())}
	let s = memory.NewSequence(failing)
	let c = testing.NewVirtualClock(0)
	let _ = llm.Retry{Inner: s, Schedule: schedule.Recurs(4), Clock: c, Sleep: c}.Stream(r, func(d llm.Delta) fails llm.SinkFailed {}) catch {
	case llm.SinkFailed:
		return false
	case _:
		let a = memory.NewSequence(failing)
		let b = memory.NewSequence([]memory.Reply{ok("other", none())})
		let _ = llm.Fallback{Members: []llm.Model{a, b}}.Stream(r, func(d llm.Delta) fails llm.SinkFailed {}) catch {
		case llm.SinkFailed:
			return false
		case _:
			return memory.Asks(s) == 1 && memory.Asks(a) == 1 && memory.Asks(b) == 0
		}
		return false
	}
	false
}

test MemoryNoReplayAfterFirstDelta(text string, f llm.Failure) { assert noReplayAfterFirstDelta(text, f) }

func fallbackIsFirstSuccess(fs []llm.Failure, succeeds bool) bool {
	let mut members = []llm.Model{}
	let mut seqs = []memory.Sequence{}
	for f in fs {
		let s = memory.NewSequence([]memory.Reply{memory.Breaks{Deltas: []llm.Delta{}, Failure: f}})
		seqs = seqs.Append(s)
		members = members.Append(s)
	}
	let last = memory.NewSequence([]memory.Reply{ok("last", none())})
	if succeeds {
		members = members.Append(last)
	}
	let t = llm.Fallback{Members: members}.Generate(plain("q")) catch {
	case f:
		let got llm.Failure = f
		let want llm.Failure = if len(fs) == 0 { llm.InvalidRequest{Reason: "a fallback with no member"} } else { bare(fs[len(fs) - 1]) }
		return !succeeds && got == want && askedOnce(seqs)
	}
	succeeds && t.Text == "last" && askedOnce(seqs) && memory.Asks(last) == 1
}

test MemoryFallbackIsFirstSuccess(fs []llm.Failure, succeeds bool) { assert fallbackIsFirstSuccess(fs, succeeds) }

func metered(m llm.Metered, r llm.Request, stream bool) llm.Turn fails llm.RateLimited | llm.Overloaded | llm.Unauthorized | llm.Refused | llm.Truncated | llm.Transport | llm.InvalidRequest | llm.InvalidResponse | llm.UnsupportedSchema {
	if stream {
		return try m.Stream(r, func(d llm.Delta) fails llm.SinkFailed {}) catch {
		case llm.SinkFailed:
			Unreachable("a sink that drops every delta failed")
		}
	}
	try m.Generate(r)
}

type metering { Transparent bool, Summed bool }

func meter(us []llm.Usage, f llm.Failure, stream bool) metering {
	let mut replies = []memory.Reply{}
	let mut want = none()
	for i, u in us {
		replies = replies.Append(ok("t\(i)", u))
		want = llm.Usage{Input: want.Input + u.Input, Output: want.Output + u.Output, CacheRead: want.CacheRead + u.CacheRead, CacheWrite: want.CacheWrite + u.CacheWrite}
	}
	let m = llm.NewMetered(memory.NewSequence(replies.Append(memory.Breaks{Deltas: []llm.Delta{}, Failure: f})))
	let alone = memory.NewSequence(replies.Append(memory.Breaks{Deltas: []llm.Delta{}, Failure: f}))
	let r = plain("q")
	let mut same = true
	for i, _ in us {
		let t = metered(m, r, stream) catch {
		case _:
			return metering{Transparent: false, Summed: false}
		}
		let b = alone.Generate(r) catch {
		case _:
			return metering{Transparent: false, Summed: false}
		}
		same = same && t == b && t.Text == "t\(i)"
	}
	let _ = metered(m, r, stream) catch {
	case g:
		let got llm.Failure = g
		let _ = alone.Generate(r) catch {
		case h:
			let bare llm.Failure = h
			return metering{Transparent: same && got == bare, Summed: llm.Used(m) == want}
		}
		return metering{Transparent: false, Summed: llm.Used(m) == want}
	}
	metering{Transparent: false, Summed: llm.Used(m) == want}
}

test MemoryUsageAdds(us []llm.Usage, f llm.Failure) { assert meter(us, f, false).Summed && meter(us, f, true).Summed }

test MemoryMeteredIsTransparent(us []llm.Usage, f llm.Failure) { assert meter(us, f, false).Transparent && meter(us, f, true).Transparent }

type seen { Deltas []llm.Delta, End recorded.End }

func asked(m llm.Model, r llm.Request, stream bool) seen {
	if !stream {
		let g = m.Generate(r) catch {
		case f:
			let failure llm.Failure = f
			return seen{Deltas: []llm.Delta{}, End: recorded.Failed{Failure: failure}}
		}
		return seen{Deltas: []llm.Delta{}, End: recorded.Answered{Turn: g}}
	}
	let mut ds = []llm.Delta{}
	let t = m.Stream(r, func(d llm.Delta) fails llm.SinkFailed { ds = ds.Append(d) }) catch {
	case llm.SinkFailed:
		Unreachable("a sink that keeps every delta failed")
	case f:
		let failure llm.Failure = f
		return seen{Deltas: ds, End: recorded.Failed{Failure: failure}}
	}
	seen{Deltas: ds, End: recorded.Answered{Turn: t}}
}

func request(pick int) llm.Request {
	let text = "q\((pick % 3 + 3) % 3)"
	llm.Request{System: "", Messages: []llm.Message{llm.User{Text: text}}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0}
}

func four(picks []int) []int { if len(picks) > 4 { picks[:4] } else { picks } }

func lawReplayIsTheRecording(replies []memory.Reply, drawn []int, recordBy []bool, replayBy []bool) bool {
	let picks = four(drawn)
	let rec = recorded.NewRecord(memory.NewSequence(replies))
	let alone = memory.NewSequence(replies)
	let mut got = []seen{}
	for k, p in picks {
		let want = asked(alone, request(p), true)
		if asked(rec, request(p), !flag(recordBy, k)) != shown(want, !flag(recordBy, k)) {
			return false
		}
		got = got.Append(want)
	}
	let replay = recorded.NewReplay(recorded.Exchanges(rec))
	let mut i = 0
	for want in 0..3 {
		for k, p in picks {
			if (p % 3 + 3) % 3 != want {
				continue
			}
			let stream = !flag(replayBy, i)
			i = i + 1
			if asked(replay, request(p), stream) != shown(got[k], stream) {
				return false
			}
		}
	}
	true
}

func flag(fs []bool, i int) bool { i < len(fs) && fs[i] }

func shown(s seen, stream bool) seen { if stream { s } else { seen{Deltas: []llm.Delta{}, End: s.End} } }

func lawFileIsTheRecording(x recorded.Exchange) bool {
	let y = recorded.Exchange{Digest: "d", Deltas: []llm.Delta{}, End: recorded.Failed{Failure: llm.Overloaded}}
	let back = recorded.Read(recorded.Write([]recorded.Exchange{x, y})) catch {
	case _:
		return false
	}
	back == []recorded.Exchange{x, y}
}

func lawDigestIsTheRequest(a llm.Request, c llm.Request, part int) bool {
	let b = spliced(a, c, part)
	(recorded.Digest(a) == recorded.Digest(b)) == (a == b)
}

func spliced(a llm.Request, c llm.Request, part int) llm.Request {
	let k = (part % 6 + 6) % 6
	llm.Request{System: if k == 0 { c.System } else { a.System }, Messages: if k == 1 { c.Messages } else { a.Messages }, Tools: if k == 2 { c.Tools } else { a.Tools }, Choice: if k == 3 { c.Choice } else { a.Choice }, Format: if k == 4 { c.Format } else { a.Format }, MaxTokens: if k == 5 { c.MaxTokens } else { a.MaxTokens }}
}

func lawUnknownRequestDiverges(replies []memory.Reply, drawn []int) bool {
	let picks = four(drawn)
	let rec = recorded.NewRecord(memory.NewSequence(replies))
	for p in picks {
		let _ = asked(rec, request(p), true)
	}
	let replay = recorded.NewReplay(recorded.Exchanges(rec))
	let never = llm.Request{System: "never", Messages: []llm.Message{}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0}
	let mut asks = []llm.Request{never}
	for p in picks {
		asks = asks.Append(request(p))
	}
	if len(picks) > 0 {
		asks = asks.Append(request(picks[0]))
	}
	for k, r in asks {
		let s = asked(replay, r, true)
		let last = k == len(asks) - 1 && len(picks) > 0
		let diverges = s.End == recorded.Failed{Failure: llm.InvalidRequest{Reason: recorded.Diverged(recorded.Digest(r))}}
		if (k == 0 || last) != diverges {
			return false
		}
	}
	true
}

func lawRecordingHoldsNoKey(key string, replies []memory.Reply) bool {
	let secret = "sk-ant-" + key
	let r = llm.Request{System: secret, Messages: []llm.Message{llm.User{Text: secret}}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0}
	let rec = recorded.NewRecord(memory.NewSequence(replies))
	let _ = asked(rec, r, true)
	let bare = recorded.NewRecord(memory.NewSequence(replies))
	let plain = llm.Request{System: "", Messages: []llm.Message{}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0}
	let _ = asked(bare, plain, true)
	strings.Contains(recorded.Lines(bare), secret) || !strings.Contains(recorded.Lines(rec), secret)
}

test RecordedReplayIsTheRecording(replies []memory.Reply, drawn []int, recordBy []bool, replayBy []bool) { assert lawReplayIsTheRecording(replies, drawn, recordBy, replayBy) }

test RecordedFileIsTheRecording(x recorded.Exchange) { assert lawFileIsTheRecording(x) }

test RecordedDigestIsTheRequest(a llm.Request, c llm.Request, part int) { assert lawDigestIsTheRequest(a, c, part) }

test RecordedUnknownRequestDiverges(replies []memory.Reply, drawn []int) { assert lawUnknownRequestDiverges(replies, drawn) }

test RecordedRecordingHoldsNoKey(key string, replies []memory.Reply) { assert lawRecordingHoldsNoKey(key, replies) }

type askProbe { Name string, Count int, Tags []string }

func askProbeSchema() jsonschema.Encoded {
	let s = jsonschema.Parse("{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\"},\"count\":{\"type\":\"integer\"},\"tags\":{\"type\":\"array\",\"items\":{\"type\":\"string\"}}},\"required\":[\"name\",\"count\",\"tags\"],\"additionalProperties\":false}") catch {
	case _:
		return jsonschema.NewEncoded([]string{})
	}
	jsonschema.Encode(s)
}

func askProbeCodec() json.Codec[askProbe] { json.Codec[askProbe]{MaxBytes: 0, MaxDepth: 0} }

func lawFormatAgreesWithCodec(v askProbe) bool {
	let text = json.Encode(askProbeCodec(), v)
	let doc = json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case _:
		return false
	}
	let back = llm.Parse(askProbeCodec(), text) catch {
	case _:
		return false
	}
	jsonschema.Conforms(jsonschema.SchemaOf(askProbeSchema()), doc) && back.Value == v && len(back.Fixes) == 0
}

func lawFormatKeepsTheRequest(system string, max int) bool {
	let r = llm.Request{System: system, Messages: []llm.Message{llm.User{Text: "hi"}}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: max}
	let f = llm.Format(r, askProbeSchema())
	let text = jsonschema.TextOf(askProbeSchema())
	let form = match f.Format {
	case llm.JSON as j:
		j.Schema == text
	case llm.Prose:
		false
	}
	form && strings.HasSuffix(f.System, text) && strings.HasPrefix(f.System, system) && f.Messages == r.Messages && f.MaxTokens == max
}

func lawParseDecodesOrFails(reply string) bool {
	let want = json.DecodeLoose(askProbeCodec(), reply) catch {
	case _:
		let _ = llm.Parse(askProbeCodec(), reply) catch {
		case llm.NoFit as n:
			return n.Reply == reply
		}
		return false
	}
	let got = llm.Parse(askProbeCodec(), reply) catch {
	case _:
		return false
	}
	got == want
}

type askAnswer = askRead{Read json.Read[askProbe]} | askNoFit{NoFit llm.NoFit} | askFailed{}

func askThrough(m memory.Sequence) askAnswer {
	with context { llm.Model: m } {
		let got = llm.Ask(askProbeCodec(), askProbeSchema(), plain("q")) catch {
		case llm.NoFit as n:
			return askNoFit{NoFit: n}
		case _:
			return askFailed
		}
		askRead{Read: got}
	}
}

func parseOf(text string) askAnswer {
	let got = llm.Parse(askProbeCodec(), text) catch {
	case llm.NoFit as n:
		return askNoFit{NoFit: n}
	}
	askRead{Read: got}
}

func lawAskIsParseOfTheReply(text string) bool {
	let m = memory.NewSequence([]memory.Reply{ok(text, none())})
	askThrough(m) == parseOf(text) && memory.Asks(m) == 1
}

func lawLengthStopIsNotParsed(v askProbe) bool {
	let text = json.Encode(askProbeCodec(), v)
	let m = memory.NewSequence([]memory.Reply{memory.Says{Deltas: []llm.Delta{llm.Text{Text: text}}, Stop: llm.Length, Usage: none()}})
	match askThrough(m) {
	case askNoFit as n:
		n.NoFit.Reply == text && n.NoFit.Path == ""
	case askRead, askFailed:
		false
	}
}

func lawAskFailsAsTheModel(f llm.Failure) bool {
	let m = memory.NewSequence([]memory.Reply{memory.Fails{Failure: f}})
	askThrough(m) == askFailed && memory.Asks(m) == 1
}

test LlmFormatAgreesWithCodec(v askProbe) { assert lawFormatAgreesWithCodec(v) }

test LlmFormatKeepsTheRequest(system string, max int) { assert lawFormatKeepsTheRequest(system, max) }

test LlmParseDecodesOrFails(reply string) { assert lawParseDecodesOrFails(reply) }

test MemoryAskIsParseOfTheReply(text string) { assert lawAskIsParseOfTheReply(text) }

test MemoryLengthStopIsNotParsed(v askProbe) { assert lawLengthStopIsNotParsed(v) }

test MemoryAskFailsAsTheModel(f llm.Failure) { assert lawAskFailsAsTheModel(f) }

func heldTexts(texts []string) []llm.Delta {
	let mut ds = []llm.Delta{}
	for s in texts {
		ds = ds.Append(llm.Text{Text: s})
	}
	ds
}

func heardTexts(h memory.Held) string {
	let mut got = []string{}
	let done = h.Stream(plain("q"), func(d llm.Delta) fails llm.SinkFailed {
		match d {
		case llm.Text as x:
			got = got.Append(x.Text)
		case llm.Reasoning, llm.CallStart, llm.CallArgs, llm.CallEnd:
		}
	}) catch {
	case _:
		return "!"
	}
	strings.Join(got, "") + "|" + done.Text
}

func isStep(got memory.HeldStep, want memory.HeldStep) bool { got == want }

func releasedArriveInOrder(texts []string) bool {
	let h = memory.Hold(memory.Says{Deltas: heldTexts(texts), Stop: llm.Ended, Usage: none()})
	let t = task.Go() { heardTexts(h) }
	let mut fine = true
	for i in 0..len(texts) {
		fine = fine && isStep(h.Next(), memory.Waits{At: i})
		h.Release(1)
		fine = fine && isStep(h.Next(), memory.Gave{At: i})
	}
	let joined = strings.Join(texts, "")
	fine && t.Wait() == joined + "|" + joined
}

test MemoryReleasedArriveInOrder(texts []string) { assert releasedArriveInOrder(texts) }

type heldHeard = heldTurn{deltas int, text string, turn string} | heldFailure{deltas int, text string, failure llm.Failure} | heldSink{}

func heldHeardOf(m llm.Model, r llm.Request) heldHeard {
	let mut ds = []llm.Delta{}
	let t = m.Stream(r, func(d llm.Delta) fails llm.SinkFailed {
		ds = ds.Append(d)
	}) catch {
	case llm.SinkFailed:
		return heldSink
	case f:
		let failure llm.Failure = f
		return heldFailure{deltas: len(ds), text: llm.Fold(ds).Text, failure: failure}
	}
	heldTurn{deltas: len(ds), text: llm.Fold(ds).Text, turn: t.Text}
}

func openLetsAllThrough(first []string, tools []string, cut bool) bool {
	let s = memory.NewScripted(first, tools, cut, []memory.Reply{})
	let h = memory.Hold(s.Replies[0])
	h.Open()
	let r = plain("q")
	heldHeardOf(h, r) == heldHeardOf(s, r)
}

test MemoryOpenLetsAllThrough(first []string, tools []string, cut bool) { assert openLetsAllThrough(first, tools, cut) }
`

// stdLLMLawStatements is the statement of each llm law, as the ledger
// writes it.
var stdLLMLawStatements = map[string]string{
	"llm Model.StreamFoldsToGenerate":     `StreamFoldsToGenerate(r Request) { foldsToGenerate(r) && foldsToGenerate(first(r)) }`,
	"llm Model.StreamReturnsItsFold":      `StreamReturnsItsFold(r Request) { returnsItsFold(r) && returnsItsFold(first(r)) }`,
	"llm Model.TruncatedIsNotATurn":       `TruncatedIsNotATurn(r Request) { notATurn(r) && notATurn(first(r)) }`,
	"llm Model.CallsAreWhole":             `CallsAreWhole(r Request) { whole(r) && whole(first(r)) }`,
	"llm FoldIsChunkFree":                 `FoldIsChunkFree(text string, cutsA []int, cutsB []int) { let a = pieces(text, cutsA) let b = pieces(text, cutsB) Fold(chunked(a)) == Fold(chunked(b)) && Fold(chunked(a)).Text == text && Fold(chunked(a)).Calls[0].Arguments == text }`,
	"llm FoldKeepsCallsApart":             `FoldKeepsCallsApart(a string, b string, cuts []int) { let pa = pieces(a, cuts) let pb = pieces(b, cuts) let mut ds = []Delta{CallStart{Index: 7, Id: "a", Name: "x"}, CallStart{Index: 3, Id: "b", Name: "y"}} let n = if len(pa) > len(pb) { len(pa) } else { len(pb) } for i in 0..n { if i < len(pb) { ds = ds.Append(CallArgs{Index: 3, Text: pb[i]}) } if i < len(pa) { ds = ds.Append(CallArgs{Index: 7, Text: pa[i]}) } } let d = Fold(ds.Append(CallEnd{Index: 3}).Append(CallEnd{Index: 7})) d.Calls == []Call{Call{Id: "a", Name: "x", Arguments: a}, Call{Id: "b", Name: "y", Arguments: b}} && len(d.Open) == 0 }`,
	"llm RequestFaultsAreUnsafe":          `RequestFaultsAreUnsafe(f Failure) { match f { case Unauthorized, Refused, InvalidRequest, UnsupportedSchema: SafetyOf(f) == Unsafe case RateLimited, Overloaded: SafetyOf(f) == Safe case Transport, Truncated, InvalidResponse: SafetyOf(f) == Unknown } }`,
	"llm AnsweredCallsAreValid":           `AnsweredCallsAreValid(text string, names []string) { let mut calls = []Call{} let mut results = []Message{} for i, n in names { calls = calls.Append(Call{Id: "call-\(i)", Name: n, Arguments: "{}"}) results = results.Append(Result{CallId: "call-\(i)", Text: n, Failed: false}) } let asked = []Message{User{Text: text}, Assistant{Text: text, Calls: calls}} if !Valid(asked.Concat(results).Append(User{Text: text})) { return false } if len(names) == 0 { return true } let early = []Message{User{Text: text}, results[0], Assistant{Text: text, Calls: calls}}.Concat(results[1:]) let missing = asked.Concat(results[1:]) let twice = asked.Concat(results).Append(results[0]) !Valid(early) && !Valid(missing) && !Valid(twice) }`,
	"llm/memory CutIsTruncated":           `CutIsTruncated(first []string, tools []string) { let s = NewScripted(first, tools, true, []Reply{}) let r = llm.Request{System: "", Messages: []llm.Message{}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0} let want = llm.Truncated{Text: strings.Join(first, "")} let _ = s.Generate(r) catch { case llm.Truncated as t: let _ = s.Stream(r, func(d llm.Delta) fails llm.SinkFailed {}) catch { case llm.Truncated as u: return t == want && u == want case _: return false } return false case _: return false } false }`,
	"llm/memory RetryFollowsSafety":       `RetryFollowsSafety(fs []llm.Failure, n int) { let budget = (n%8 + 8) % 8 let mut replies = []Reply{} for f in fs { replies = replies.Append(Breaks{Deltas: []llm.Delta{}, Failure: f}) } let s = NewSequence(replies.Append(ok("done", none()))) let c = testing.NewVirtualClock(0) let mut stop = len(fs) for i, f in fs { if i < stop && (llm.SafetyOf(f) == llm.Unsafe || i == budget) { stop = i } } let t = llm.Retry{Inner: s, Schedule: schedule.Recurs(budget), Clock: c, Sleep: c}.Generate(plain("q")) catch { case f: let got llm.Failure = f return stop < len(fs) && got == bare(fs[stop]) && Asks(s) == stop+1 } stop == len(fs) && t.Text == "done" && Asks(s) == len(fs)+1 }`,
	"llm/memory RetryKeepsUpTo":           `RetryKeepsUpTo(n int) { let k = (n%5 + 5) % 5 let mut replies = []Reply{} for _ in 0..8 { let f llm.Failure = llm.Overloaded replies = replies.Append(Fails{Failure: f}) } let s = NewSequence(replies.Append(ok("done", none()))) let c = testing.NewVirtualClock(0) let limit = duration(int64(k)*1000000000 + 500000000) let _ = llm.Retry{Inner: s, Schedule: schedule.UpTo(schedule.Spaced(1s), limit), Clock: c, Sleep: c}.Generate(plain("q")) catch { case llm.Overloaded: return Asks(s) == k+1 case _: return false } false }`,
	"llm/memory RetryHonoursAfter":        `RetryHonoursAfter(ms int) { let wait = (ms%100000 + 100000) % 100000 let c = testing.NewVirtualClock(0) let s = NewSequence([]Reply{Fails{Failure: llm.RateLimited{After: Some[int]{Value: wait}}}, ok("done", none())}) let start = c.Now() let t = llm.Retry{Inner: s, Schedule: schedule.Spaced(1ms), Clock: c, Sleep: c}.Generate(plain("q")) catch { case _: return false } let waited = c.Now().Sub(start) t.Text == "done" && waited >= duration(int64(wait)*1000000) && (wait > 0 || waited == 1ms) }`,
	"llm/memory NoReplayAfterFirstDelta":  `NoReplayAfterFirstDelta(text string, f llm.Failure) { let r = plain("q") let failing = []Reply{Breaks{Deltas: []llm.Delta{llm.Text{Text: text}}, Failure: f}, ok("again", none())} let s = NewSequence(failing) let c = testing.NewVirtualClock(0) let _ = llm.Retry{Inner: s, Schedule: schedule.Recurs(4), Clock: c, Sleep: c}.Stream(r, func(d llm.Delta) fails llm.SinkFailed {}) catch { case llm.SinkFailed: return false case _: let a = NewSequence(failing) let b = NewSequence([]Reply{ok("other", none())}) let _ = llm.Fallback{Members: []llm.Model{a, b}}.Stream(r, func(d llm.Delta) fails llm.SinkFailed {}) catch { case llm.SinkFailed: return false case _: return Asks(s) == 1 && Asks(a) == 1 && Asks(b) == 0 } return false } false }`,
	"llm/memory FallbackIsFirstSuccess":   `FallbackIsFirstSuccess(fs []llm.Failure, succeeds bool) { let mut members = []llm.Model{} let mut seqs = []Sequence{} for f in fs { let s = NewSequence([]Reply{Breaks{Deltas: []llm.Delta{}, Failure: f}}) seqs = seqs.Append(s) members = members.Append(s) } let last = NewSequence([]Reply{ok("last", none())}) if succeeds { members = members.Append(last) } let t = llm.Fallback{Members: members}.Generate(plain("q")) catch { case f: let got llm.Failure = f let want llm.Failure = if len(fs) == 0 { llm.InvalidRequest{Reason: "a fallback with no member"} } else { bare(fs[len(fs)-1]) } return !succeeds && got == want && askedOnce(seqs) } succeeds && t.Text == "last" && askedOnce(seqs) && Asks(last) == 1 }`,
	"llm/memory UsageAdds":                `UsageAdds(us []llm.Usage, f llm.Failure) { meter(us, f, false).Summed && meter(us, f, true).Summed }`,
	"llm/memory MeteredIsTransparent":     `MeteredIsTransparent(us []llm.Usage, f llm.Failure) { meter(us, f, false).Transparent && meter(us, f, true).Transparent }`,
	"llm FormatAgreesWithCodec":           `FormatAgreesWithCodec(v askProbe) { let text = json.Encode(askCodec(), v) let doc = json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, text) catch { case _: return false } let back = Parse(askCodec(), text) catch { case _: return false } jsonschema.Conforms(jsonschema.SchemaOf(askProbeSchema()), doc) && back.Value == v && len(back.Fixes) == 0 }`,
	"llm FormatKeepsTheRequest":           `FormatKeepsTheRequest(system string, max int) { let r = Request{System: system, Messages: []Message{User{Text: "hi"}}, Tools: []ToolSpec{}, Choice: Auto, Format: Prose, MaxTokens: max} let f = Format(r, askProbeSchema()) let text = jsonschema.TextOf(askProbeSchema()) let form = match f.Format { case JSON as j: j.Schema == text case Prose: false } form && strings.HasSuffix(f.System, text) && strings.HasPrefix(f.System, system) && f.Messages == r.Messages && f.MaxTokens == max }`,
	"llm ParseDecodesOrFails":             `ParseDecodesOrFails(reply string) { let want = json.DecodeLoose(askCodec(), reply) catch { case _: let _ = Parse(askCodec(), reply) catch { case NoFit as n: return n.Reply == reply } return false } let got = Parse(askCodec(), reply) catch { case _: return false } got == want }`,
	"llm/memory AskIsParseOfTheReply":     `AskIsParseOfTheReply(text string) { let m = NewSequence([]Reply{ok(text, none())}) askOf(m, plain("q")) == parsing(text) && Asks(m) == 1 }`,
	"llm/memory LengthStopIsNotParsed":    `LengthStopIsNotParsed(v asked) { let text = json.Encode(askedCodec(), v) let m = NewSequence([]Reply{Says{Deltas: []llm.Delta{llm.Text{Text: text}}, Stop: llm.Length, Usage: none()}}) match askOf(m, plain("q")).NoFit { case Some[llm.NoFit] as n: n.Value.Reply == text && n.Value.Path == "" case None: false } }`,
	"llm/memory AskFailsAsTheModel":       `AskFailsAsTheModel(f llm.Failure) { let m = NewSequence([]Reply{Fails{Failure: f}}) askOf(m, plain("q")).Other == "failure" && Asks(m) == 1 }`,
	"llm/memory OpenLetsAllThrough":       `OpenLetsAllThrough(first []string, tools []string, cut bool) { let s = NewScripted(first, tools, cut, []Reply{}) let h = Hold(s.Replies[0]) h.Open() let r = plain("q") heardOf(h, r) == heardOf(s, r) }`,
	"llm/memory ReleasedArriveInOrder":    `ReleasedArriveInOrder(texts []string) { let h = Hold(Says{Deltas: heldTexts(texts), Stop: llm.Ended, Usage: none()}) let t = task.Go() { heardTexts(h) } let mut fine = true for i in 0..len(texts) { fine = fine && isStep(h.Next(), Waits{At: i}) h.Release(1) fine = fine && isStep(h.Next(), Gave{At: i}) } let joined = strings.Join(texts, "") fine && t.Wait() == joined + "|" + joined }`,
	"llm/recorded ReplayIsTheRecording":   `ReplayIsTheRecording(replies []memory.Reply, drawn []int, recordBy []bool, replayBy []bool) { let picks = four(drawn) let rec = NewRecord(memory.NewSequence(replies)) let alone = memory.NewSequence(replies) let mut got = []seen{} for k, p in picks { let want = asked(alone, request(p), true) if asked(rec, request(p), !flag(recordBy, k)) != shown(want, !flag(recordBy, k)) { return false } got = got.Append(want) } let replay = NewReplay(Exchanges(rec)) let mut i = 0 for want in 0..3 { for k, p in picks { if (p%3+3)%3 != want { continue } let stream = !flag(replayBy, i) i = i + 1 if asked(replay, request(p), stream) != shown(got[k], stream) { return false } } } true }`,
	"llm/recorded FileIsTheRecording":     `FileIsTheRecording(x Exchange) { let y = Exchange{Digest: "d", Deltas: []llm.Delta{}, End: Failed{Failure: llm.Overloaded}} let back = Read(Write([]Exchange{x, y})) catch { case _: return false } back == []Exchange{x, y} }`,
	"llm/recorded DigestIsTheRequest":     `DigestIsTheRequest(a llm.Request, c llm.Request, part int) { let b = spliced(a, c, part) (Digest(a) == Digest(b)) == (a == b) }`,
	"llm/recorded UnknownRequestDiverges": `UnknownRequestDiverges(replies []memory.Reply, drawn []int) { let picks = four(drawn) let rec = NewRecord(memory.NewSequence(replies)) for p in picks { let _ = asked(rec, request(p), true) } let replay = NewReplay(Exchanges(rec)) let never = llm.Request{System: "never", Messages: []llm.Message{}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0} let mut asks = []llm.Request{never} for p in picks { asks = asks.Append(request(p)) } if len(picks) > 0 { asks = asks.Append(request(picks[0])) } for k, r in asks { let s = asked(replay, r, true) let last = k == len(asks)-1 && len(picks) > 0 let diverges = s.End == Failed{Failure: llm.InvalidRequest{Reason: Diverged(Digest(r))}} if (k == 0 || last) != diverges { return false } } true }`,
	"llm/recorded RecordingHoldsNoKey":    `RecordingHoldsNoKey(key string, replies []memory.Reply) { let secret = "sk-ant-" + key let r = llm.Request{System: secret, Messages: []llm.Message{llm.User{Text: secret}}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0} let rec = NewRecord(memory.NewSequence(replies)) let _ = asked(rec, r, true) let bare = NewRecord(memory.NewSequence(replies)) let plain = llm.Request{System: "", Messages: []llm.Message{}, Tools: []llm.ToolSpec{}, Choice: llm.Auto, Format: llm.Prose, MaxTokens: 0} let _ = asked(bare, plain, true) strings.Contains(Lines(bare), secret) || !strings.Contains(Lines(rec), secret) }`,
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

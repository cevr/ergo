package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdProofLawsProbe restates the laws whose runner is TestStdProofLawsRun
// as Ergo tests. A member law runs on a provider of each kind the probe
// draws; a law over a package's private helpers restates them.
const stdProofLawsProbe = `import fs
import fstest
import gen
import proof
import proof/file
import proof/memory
import rand
import strconv
import strings
import task
import time
import totp

// provider is a store of each kind, by pick: a memory store, or a file
// store over its own fstest.Memory disk.
func provider(pick int) proof.Store {
	if ((pick%2)+2)%2 == 0 {
		return memory.New()
	}
	let disk = fstest.NewMemory()
	file.Open(disk, disk, "proofs.log") catch {
	case proof.Failed:
		Unreachable("an empty disk opens")
	}
}

func storeRedeemOnce(s proof.Store, id string, purpose string, code string, sec int64) bool fails proof.Failed {
	let now = time.Unix(sec % 1000000000, 0)
	let d = proof.DigestOf(purpose, code)
	try s.Issue(id, d, now.Add(1m), 3) catch {
	case proof.Conflict:
		return true
	}
	try s.Redeem(id, d, now) catch {
	case proof.Unknown, proof.Expired, proof.Spent, proof.TooManyAttempts, proof.Wrong:
		return false
	}
	try s.Redeem(id, d, now) catch {
	case proof.Spent:
		return true
	case proof.Unknown, proof.Expired, proof.TooManyAttempts, proof.Wrong:
		return false
	}
	false
}

test ProofStoreRedeemOnce(pick int, id string, purpose string, code string, sec int64) {
	let ok = storeRedeemOnce(provider(pick), id, purpose, code, sec) catch {
	case proof.Failed:
		false
	}
	assert ok
}

func storeExpiredFails(s proof.Store, id string, code string, sec int64) bool fails proof.Failed {
	let now = time.Unix(sec % 1000000000, 0)
	let d = proof.DigestOf("p", code)
	try s.Issue(id, d, now, 3) catch {
	case proof.Conflict:
		return true
	}
	try s.Issue(id + "'", d, now, 3) catch {
	case proof.Conflict:
		return true
	}
	try s.Redeem(id + "'", d, now.Add(-1ns)) catch {
	case proof.Unknown, proof.Expired, proof.Spent, proof.TooManyAttempts, proof.Wrong:
		return false
	}
	try s.Redeem(id, d, now) catch {
	case proof.Expired:
		return true
	case proof.Unknown, proof.Spent, proof.TooManyAttempts, proof.Wrong:
		return false
	}
	false
}

test ProofStoreExpiredFails(pick int, id string, code string, sec int64) {
	let ok = storeExpiredFails(provider(pick), id, code, sec) catch {
	case proof.Failed:
		false
	}
	assert ok
}

func storeAttemptsAreBounded(s proof.Store, id string, code string, n int) bool fails proof.Failed {
	let limit = 1 + ((n%5)+5)%5
	let now = time.Unix(0, 0)
	let d = proof.DigestOf("p", code)
	try s.Issue(id, d, now.Add(1m), limit) catch {
	case proof.Conflict:
		return true
	}
	for i in 0..limit {
		try s.Redeem(id, proof.DigestOf("p", code + "x"), now) catch {
		case proof.Wrong as w:
			if w.Left != limit - i - 1 {
				return false
			}
		case proof.Unknown, proof.Expired, proof.Spent, proof.TooManyAttempts:
			return false
		}
	}
	try s.Redeem(id, d, now) catch {
	case proof.TooManyAttempts:
		return true
	case proof.Unknown, proof.Expired, proof.Spent, proof.Wrong:
		return false
	}
	false
}

test ProofStoreAttemptsAreBounded(pick int, id string, code string, n int) {
	let ok = storeAttemptsAreBounded(provider(pick), id, code, n) catch {
	case proof.Failed:
		false
	}
	assert ok
}

func storeWrongPurposeFails(s proof.Store, id string, code string) bool fails proof.Failed {
	let now = time.Unix(0, 0)
	try s.Issue(id, proof.DigestOf("confirm", code), now.Add(1m), 3) catch {
	case proof.Conflict:
		return true
	}
	try s.Redeem(id, proof.DigestOf("reset", code), now) catch {
	case proof.Wrong:
	case proof.Unknown, proof.Expired, proof.Spent, proof.TooManyAttempts:
		return false
	}
	try s.Redeem(id, proof.DigestOf("confirm", code), now) catch {
	case proof.Unknown, proof.Expired, proof.Spent, proof.TooManyAttempts, proof.Wrong:
		return false
	}
	true
}

test ProofStoreWrongPurposeFails(pick int, id string, code string) {
	let ok = storeWrongPurposeFails(provider(pick), id, code) catch {
	case proof.Failed:
		false
	}
	assert ok
}

func storeClaimOnce(s proof.Store, key string, sec int64) bool fails proof.Failed {
	let now = time.Unix(sec % 1000000000, 0)
	try s.Claim(key, now.Add(1m), now) catch {
	case proof.Spent:
		return true
	}
	try s.Claim(key, now.Add(1m), now.Add(59s)) catch {
	case proof.Spent:
		try s.Claim(key, now.Add(2m), now.Add(1m)) catch {
		case proof.Spent:
			return false
		}
		return true
	}
	false
}

test ProofStoreClaimOnce(pick int, key string, sec int64) {
	let ok = storeClaimOnce(provider(pick), key, sec) catch {
	case proof.Failed:
		false
	}
	assert ok
}

// totp's law helpers, restated.
// vector is one row of RFC 6238's appendix B for SHA-1: the Unix time and
// the eight-digit code.
type vector { Sec int64, Code string }

func lawMatchesRFC6238() bool {
	let p = totp.NewParams(8, 30s, 1) catch {
	case totp.BadParams:
		return false
	}
	let rows = []vector{
		vector{Sec: 59, Code: "94287082"},
		vector{Sec: 1111111109, Code: "07081804"},
		vector{Sec: 1111111111, Code: "14050471"},
		vector{Sec: 1234567890, Code: "89005924"},
		vector{Sec: 2000000000, Code: "69279037"},
		vector{Sec: 20000000000, Code: "65353130"},
	}
	for r in rows {
		if totp.Code(p, "12345678901234567890", time.Unix(r.Sec, 0)) != r.Code {
			return false
		}
	}
	true
}

test TotpMatchesRFC6238() {
	assert lawMatchesRFC6238()
}

func lawVerifyOfCode(secret string, sec int64, drift int) bool {
	let p = totp.Default()
	let t = time.Unix(((sec%10000000000)+10000000000)%10000000000, 0)
	let step = totp.StepOf(p, t)
	let code = totp.Code(p, secret, t)
	let later = t.Add(time.Times(30s, ((drift%2)+2)%2))
	match totp.Verify(p, secret, code, later) {
	case Some[int64] as s:
		s.Value == step || totp.CodeOf(p, secret, s.Value) == code
	case None:
		false
	}
}

test TotpVerifyOfCode(secret string, sec int64, drift int) {
	assert lawVerifyOfCode(secret, sec, drift)
}

func lawSameWorkForEveryCode(secret string, sec int64, w int) bool {
	let p = totp.NewParams(6, 30s, ((w%3)+3)%3) catch {
	case totp.BadParams:
		return false
	}
	let now = time.Unix(((sec%10000000000)+10000000000)%10000000000, 0)
	let at = totp.StepOf(p, now)
	let k = totp.KeyOf(secret)
	let first = totp.CodeOf(p, secret, at - int64(totp.Window(p)))
	let last = totp.CodeOf(p, secret, at + int64(totp.Window(p)))
	let mut none = "000000"
	for n in 0..2*totp.Window(p) + 2 {
		let text = strconv.Itoa(n)
		let c = strings.Repeat("0", 6 - len(text)) + text
		let found, _ = totp.VerifyWork(p, k, c, now)
		if found == None {
			none = c
		}
	}
	let a, wa = totp.VerifyWork(p, k, first, now)
	let b, wb = totp.VerifyWork(p, k, last, now)
	let c, wc = totp.VerifyWork(p, k, none, now)
	a != None && b != None && c == None && wa == 2*totp.Window(p) + 1 && wb == wa && wc == wa
}

test TotpSameWorkForEveryCode(secret string, sec int64, w int) {
	assert lawSameWorkForEveryCode(secret, sec, w)
}

func lawOutsideWindowFails(secret string, sec int64, far int) bool {
	let p = totp.Default()
	let now = time.Unix(((sec%10000000000)+10000000000)%10000000000, 0)
	let at = totp.StepOf(p, now)
	let k = int64(2 + ((far%20)+20)%20)
	for sign in []int64{-1, 1} {
		let code = totp.CodeOf(p, secret, at + sign*k)
		let same = totp.CodeOf(p, secret, at - 1) == code || totp.CodeOf(p, secret, at) == code || totp.CodeOf(p, secret, at + 1) == code
		if !same && totp.Verify(p, secret, code, now) != None {
			return false
		}
	}
	true
}

test TotpOutsideWindowFails(secret string, sec int64, far int) {
	assert lawOutsideWindowFails(secret, sec, far)
}

func lawShortCodeFails(secret string, sec int64) bool {
	let p = totp.Default()
	let now = time.Unix(((sec%10000000000)+10000000000)%10000000000, 0)
	totp.Verify(p, secret, totp.Code(p, secret, now)[1:], now) == None
}

test TotpShortCodeFails(secret string, sec int64) {
	assert lawShortCodeFails(secret, sec)
}

func lawNewParamsBoundsTheWindow(window int) bool {
	let w = ((window%40)+40)%40 - 20
	let _ = totp.NewParams(6, 30s, w) catch {
	case totp.BadParams:
		return w < 0 || w > 2
	}
	w >= 0 && w <= 2
}

test TotpNewParamsBoundsTheWindow(window int) {
	assert lawNewParamsBoundsTheWindow(window)
}

func lawStepsAreWholePeriods(sec int64) bool {
	let p = totp.Default()
	let s = sec % 100000000000
	let step = totp.StepOf(p, time.Unix(s, 0))
	step * 30 <= s && s < (step + 1) * 30
}

test TotpStepsAreWholePeriods(sec int64) {
	assert lawStepsAreWholePeriods(sec)
}

func lawURIOfTheRFCSecret() bool {
	totp.URI(totp.Default(), "Example Co", "a@b.c", "12345678901234567890") == "otpauth://totp/Example%20Co:a%40b.c?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=Example%20Co&algorithm=SHA1&digits=6&period=30"
}

test TotpURIOfTheRFCSecret() {
	assert lawURIOfTheRFCSecret()
}

// proof/memory's law helpers, restated.
// redeems is whether a redeem of id with d won.
func redeems(s memory.Store, id string, d proof.Digest, now time.Time) bool {
	s.Redeem(id, d, now) catch {
	case proof.Unknown, proof.Expired, proof.Spent, proof.TooManyAttempts, proof.Wrong, proof.Failed:
		return false
	}
	true
}

func drawDest() string needs gen.Draws {
	strings.Join(gen.ListOf(0, 8, func() string needs gen.Draws { gen.OneOf([]string{"a", "b", "0", "@", "."}) }), "")
}

test MemorySentCodeRedeems {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let dest = gen.Named("dest", func(v string) string { "\"\(v)\"" }, drawDest)
		let s0 = gen.Below(uint64(0))
		let s = memory.New()
		let r = proof.NewRecorder()
		with context { rand.Secure: rand.NewSeededSecure(s0), time.Clock: time.NewManual(0) } {
			let c = proof.SendCode(s, r, dest, "confirm", 10m, 3) catch {
			case proof.Conflict, proof.Failed, proof.Undelivered:
				return false
			}
			let out = r.Outbox()
			if len(out) != 1 || out[0].Dest != dest || out[0].Purpose != "confirm" || len(out[0].Code) != 6 {
				return false
			}
			gen.Draws.Show("code", out[0].Code)
			proof.RedeemCode(s, proof.Challenge{ID: out[0].Code}, "confirm", out[0].Code) catch {
			case proof.Unknown:
			case proof.Expired, proof.Spent, proof.TooManyAttempts, proof.Wrong, proof.Failed:
				return false
			}
			proof.RedeemCode(s, c, "confirm", out[0].Code) catch {
			case proof.Unknown, proof.Expired, proof.Spent, proof.TooManyAttempts, proof.Wrong, proof.Failed:
				return false
			}
			proof.RedeemCode(s, c, "confirm", out[0].Code) catch {
			case proof.Spent:
				return true
			case proof.Unknown, proof.Expired, proof.TooManyAttempts, proof.Wrong, proof.Failed:
				return false
			}
			false
		}
	})) == ""
}

func lawStepIsUsedOnce(secret string, sec int64) bool {
	let s = memory.New()
	let p = totp.Default()
	let m = time.NewManual(((sec%1000000000)+1000000000)%1000000000)
	with context { time.Clock: m } {
		let code = totp.Code(p, secret, m.Now())
		proof.VerifyCode(s, p, "a", secret, code) catch {
		case proof.Wrong, proof.Spent, proof.Failed:
			return false
		}
		proof.VerifyCode(s, p, "b", secret, code) catch {
		case proof.Wrong, proof.Spent, proof.Failed:
			return false
		}
		m.Advance(30s)
		proof.VerifyCode(s, p, "a", secret, code) catch {
		case proof.Spent:
			return true
		case proof.Wrong, proof.Failed:
			return false
		}
		false
	}
}

test MemoryStepIsUsedOnce(secret string, sec int64) {
	assert lawStepIsUsedOnce(secret, sec)
}

func lawRedeemHasOneWinner(id string, code string) bool {
	let s = memory.New()
	let now = time.Unix(0, 0)
	let d = proof.DigestOf("p", code)
	s.Issue(id, d, now.Add(1m), 3) catch {
	case proof.Conflict, proof.Failed:
		return false
	}
	let ta = task.Go() { redeems(s, id, d, now) }
	let tb = task.Go() { redeems(s, id, d, now) }
	ta.Wait() != tb.Wait()
}

test MemoryRedeemHasOneWinner(id string, code string) {
	assert lawRedeemHasOneWinner(id, code)
}

// proof/file's law helpers, restated.
// script is one step of a FileIsMemory script: wait Wait seconds, then
// issue, redeem (with the right code or a wrong one) or claim, or open the
// file store again.
type script = issue{Pick int, Wait int} | redeem{Pick int, Right bool, Wait int} | claim{Pick int, Wait int} | reopen{}

// later is t plus sec seconds, with no monotonic reading, as a time read
// back from the log is: == of two outcomes compares it.
func later(t time.Time, sec int) time.Time { time.Unix(time.UnixSeconds(t) + int64(((sec%20)+20)%20), time.Nanosecond(t) + 1000) }

// issued is whether s kept a proof of code 123456 under id for 30 seconds
// and two wrong codes, or found it a Conflict.
func issued(s proof.Store, id string, now time.Time) bool fails proof.Failed {
	try s.Issue(id, proof.DigestOf("p", "123456"), now.Add(30s), 2) catch {
	case proof.Conflict:
		return false
	}
	true
}

// redeemed is the answer of a redeem, as a number: 0 redeemed, 1 Unknown,
// 2 Expired, 3 Spent, 4 TooManyAttempts, 10 and up Wrong with Left.
func redeemed(s proof.Store, id string, d proof.Digest, now time.Time) int fails proof.Failed {
	try s.Redeem(id, d, now) catch {
	case proof.Unknown:
		return 1
	case proof.Expired:
		return 2
	case proof.Spent:
		return 3
	case proof.TooManyAttempts:
		return 4
	case proof.Wrong as w:
		return 10 + w.Left
	}
	0
}

// claimed is whether s claimed key for 40 seconds, or found it Spent.
func claimed(s proof.Store, key string, now time.Time) bool fails proof.Failed {
	try s.Claim(key, now.Add(40s), now) catch {
	case proof.Spent:
		return false
	}
	true
}

func lawFileIsMemory(steps []script, torn string) bool fails proof.Failed | fs.NotFound | fs.Denied | fs.Failed {
	let disk = fstest.NewMemory()
	let want = memory.New()
	let mut got = try file.Open(disk, disk, "proofs.log")
	let mut now = time.Unix(0, 0)
	for st in steps {
		match st {
		case issue as i:
			now = later(now, i.Wait)
			let id = "p\t\"\(((i.Pick%4)+4)%4)"
			if try issued(want, id, now) != try issued(got, id, now) {
				return false
			}
		case redeem as r:
			now = later(now, r.Wait)
			let id = "p\t\"\(((r.Pick%4)+4)%4)"
			let d = proof.DigestOf("p", if r.Right { "123456" } else { "654321" })
			if try redeemed(want, id, d, now) != try redeemed(got, id, d, now) {
				return false
			}
		case claim as c:
			now = later(now, c.Wait)
			let key = "k\(((c.Pick%3)+3)%3)"
			if try claimed(want, key, now) != try claimed(got, key, now) {
				return false
			}
		case reopen:
			got = try file.Open(disk, disk, "proofs.log")
		}
	}
	let edge = time.Unix(time.UnixSeconds(now) + 1, 999)
	let e = proof.DigestOf("p", "edge")
	try want.Issue("edge", e, edge, 1) catch {
	case proof.Conflict:
		return false
	}
	try got.Issue("edge", e, edge, 1) catch {
	case proof.Conflict:
		return false
	}
	try disk.AppendFile("proofs.log", "red\t" + strings.ReplaceAll(torn, "\n", ""))
	let back = try file.Open(disk, disk, "proofs.log")
	let justBefore = time.Unix(time.UnixSeconds(now) + 1, 500)
	if try redeemed(want, "edge", e, justBefore) != try redeemed(back, "edge", e, justBefore) {
		return false
	}
	for i in 0..4 {
		let id = "p\t\"\(i)"
		let d = proof.DigestOf("p", "123456")
		if try redeemed(want, id, d, now) != try redeemed(back, id, d, now) {
			return false
		}
	}
	for k in 0..3 {
		if try claimed(want, "k\(k)", now) != try claimed(back, "k\(k)", now) {
			return false
		}
	}
	true
}

test FileFileIsMemory(steps []script, torn string) {
	let ok = lawFileIsMemory(steps, torn) catch {
	case proof.Failed, fs.NotFound, fs.Denied, fs.Failed:
		false
	}
	assert ok
}

func lawOpenRefusesABrokenLine(id string, line string) bool fails proof.Failed | fs.NotFound | fs.Denied | fs.Failed {
	let disk = fstest.NewMemory()
	let s = try file.Open(disk, disk, "proofs.log")
	try s.Issue(id, proof.DigestOf("p", "1"), time.Unix(60, 0), 3) catch {
	case proof.Conflict:
		return false
	}
	try disk.AppendFile("proofs.log", "x" + strings.ReplaceAll(line, "\n", "") + "\n")
	let _ = file.Open(disk, disk, "proofs.log") catch {
	case proof.Failed:
		return true
	}
	false
}

test FileOpenRefusesABrokenLine(id string, line string) {
	let ok = lawOpenRefusesABrokenLine(id, line) catch {
	case proof.Failed, fs.NotFound, fs.Denied, fs.Failed:
		false
	}
	assert ok
}

func lawLogHoldsNoCode(code string) bool fails proof.Failed | fs.NotFound | fs.Denied | fs.Failed {
	let secret = "code:" + code
	let d = proof.DigestOf("p", secret)
	let disk = fstest.NewMemory()
	let s = try file.Open(disk, disk, "proofs.log")
	try s.Issue("id", d, time.Unix(60, 0), 3) catch {
	case proof.Conflict:
		return false
	}
	let log = try disk.ReadFile("proofs.log")
	!strings.Contains(log, secret) && strings.Contains(log, proof.DigestText(d))
}

test FileLogHoldsNoCode(code string) {
	let ok = lawLogHoldsNoCode(code) catch {
	case proof.Failed, fs.NotFound, fs.Denied, fs.Failed:
		false
	}
	assert ok
}

`

// stdProofLawStatements is the statement of each law the probe restates,
// by ledger key; lawledger.Run holds it equal to the ledger.
var stdProofLawStatements = map[string]string{
	"totp MatchesRFC6238":               `MatchesRFC6238() { let p = NewParams(8, 30s, 1) catch { case BadParams: return false } let rows = []vector{ vector{Sec: 59, Code: "94287082"}, vector{Sec: 1111111109, Code: "07081804"}, vector{Sec: 1111111111, Code: "14050471"}, vector{Sec: 1234567890, Code: "89005924"}, vector{Sec: 2000000000, Code: "69279037"}, vector{Sec: 20000000000, Code: "65353130"}, } for r in rows { if Code(p, "12345678901234567890", time.Unix(r.Sec, 0)) != r.Code { return false } } true }`,
	"totp VerifyOfCode":                 `VerifyOfCode(secret string, sec int64, drift int) { let p = Default() let t = time.Unix(((sec%10000000000)+10000000000)%10000000000, 0) let step = StepOf(p, t) let code = Code(p, secret, t) let later = t.Add(time.Times(30s, ((drift%2)+2)%2)) match Verify(p, secret, code, later) { case Some[int64] as s: s.Value == step || CodeOf(p, secret, s.Value) == code case None: false } }`,
	"totp SameWorkForEveryCode":         `SameWorkForEveryCode(secret string, sec int64, w int) { let p = NewParams(6, 30s, ((w%3)+3)%3) catch { case BadParams: return false } let now = time.Unix(((sec%10000000000)+10000000000)%10000000000, 0) let at = StepOf(p, now) let k = KeyOf(secret) let first = CodeOf(p, secret, at - int64(Window(p))) let last = CodeOf(p, secret, at + int64(Window(p))) let mut none = "000000" for n in 0..2*Window(p) + 2 { let text = strconv.Itoa(n) let c = strings.Repeat("0", 6 - len(text)) + text let found, _ = VerifyWork(p, k, c, now) if found == None { none = c } } let a, wa = VerifyWork(p, k, first, now) let b, wb = VerifyWork(p, k, last, now) let c, wc = VerifyWork(p, k, none, now) a != None && b != None && c == None && wa == 2*Window(p) + 1 && wb == wa && wc == wa }`,
	"totp OutsideWindowFails":           `OutsideWindowFails(secret string, sec int64, far int) { let p = Default() let now = time.Unix(((sec%10000000000)+10000000000)%10000000000, 0) let at = StepOf(p, now) let k = int64(2 + ((far%20)+20)%20) for sign in []int64{-1, 1} { let code = CodeOf(p, secret, at + sign*k) let same = CodeOf(p, secret, at - 1) == code || CodeOf(p, secret, at) == code || CodeOf(p, secret, at + 1) == code if !same && Verify(p, secret, code, now) != None { return false } } true }`,
	"totp ShortCodeFails":               `ShortCodeFails(secret string, sec int64) { let p = Default() let now = time.Unix(((sec%10000000000)+10000000000)%10000000000, 0) Verify(p, secret, Code(p, secret, now)[1:], now) == None }`,
	"totp NewParamsBoundsTheWindow":     `NewParamsBoundsTheWindow(window int) { let w = ((window%40)+40)%40 - 20 let _ = NewParams(6, 30s, w) catch { case BadParams: return w < 0 || w > 2 } w >= 0 && w <= 2 }`,
	"totp StepsAreWholePeriods":         `StepsAreWholePeriods(sec int64) { let p = Default() let s = sec % 100000000000 let step = StepOf(p, time.Unix(s, 0)) step * 30 <= s && s < (step + 1) * 30 }`,
	"totp URIOfTheRFCSecret":            `URIOfTheRFCSecret() { URI(Default(), "Example Co", "a@b.c", "12345678901234567890") == "otpauth://totp/Example%20Co:a%40b.c?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=Example%20Co&algorithm=SHA1&digits=6&period=30" }`,
	"proof Store.RedeemOnce":            `RedeemOnce(id string, purpose string, code string, sec int64) { let now = time.Unix(sec % 1000000000, 0) let d = DigestOf(purpose, code) try Issue(id, d, now.Add(1m), 3) catch { case Conflict: return true } try Redeem(id, d, now) catch { case Unknown, Expired, Spent, TooManyAttempts, Wrong: return false } try Redeem(id, d, now) catch { case Spent: return true case Unknown, Expired, TooManyAttempts, Wrong: return false } false }`,
	"proof Store.ExpiredFails":          `ExpiredFails(id string, code string, sec int64) { let now = time.Unix(sec % 1000000000, 0) let d = DigestOf("p", code) try Issue(id, d, now, 3) catch { case Conflict: return true } try Issue(id + "'", d, now, 3) catch { case Conflict: return true } try Redeem(id + "'", d, now.Add(-1ns)) catch { case Unknown, Expired, Spent, TooManyAttempts, Wrong: return false } try Redeem(id, d, now) catch { case Expired: return true case Unknown, Spent, TooManyAttempts, Wrong: return false } false }`,
	"proof Store.AttemptsAreBounded":    `AttemptsAreBounded(id string, code string, n int) { let limit = 1 + ((n%5)+5)%5 let now = time.Unix(0, 0) let d = DigestOf("p", code) try Issue(id, d, now.Add(1m), limit) catch { case Conflict: return true } for i in 0..limit { try Redeem(id, DigestOf("p", code + "x"), now) catch { case Wrong as w: if w.Left != limit - i - 1 { return false } case Unknown, Expired, Spent, TooManyAttempts: return false } } try Redeem(id, d, now) catch { case TooManyAttempts: return true case Unknown, Expired, Spent, Wrong: return false } false }`,
	"proof Store.WrongPurposeFails":     `WrongPurposeFails(id string, code string) { let now = time.Unix(0, 0) try Issue(id, DigestOf("confirm", code), now.Add(1m), 3) catch { case Conflict: return true } try Redeem(id, DigestOf("reset", code), now) catch { case Wrong: case Unknown, Expired, Spent, TooManyAttempts: return false } try Redeem(id, DigestOf("confirm", code), now) catch { case Unknown, Expired, Spent, TooManyAttempts, Wrong: return false } true }`,
	"proof Store.ClaimOnce":             `ClaimOnce(key string, sec int64) { let now = time.Unix(sec % 1000000000, 0) try Claim(key, now.Add(1m), now) catch { case Spent: return true } try Claim(key, now.Add(1m), now.Add(59s)) catch { case Spent: try Claim(key, now.Add(2m), now.Add(1m)) catch { case Spent: return false } return true } false }`,
	"proof/memory SentCodeRedeems":      `SentCodeRedeems() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let dest = gen.Named("dest", func(v string) string { "\"\(v)\"" }, drawDest) let s0 = gen.Below(uint64(0)) let s = New() let r = proof.NewRecorder() with context { rand.Secure: rand.NewSeededSecure(s0), time.Clock: time.NewManual(0) } { let c = proof.SendCode(s, r, dest, "confirm", 10m, 3) catch { case proof.Conflict, proof.Failed, proof.Undelivered: return false } let out = r.Outbox() if len(out) != 1 || out[0].Dest != dest || out[0].Purpose != "confirm" || len(out[0].Code) != 6 { return false } gen.Draws.Show("code", out[0].Code) proof.RedeemCode(s, proof.Challenge{ID: out[0].Code}, "confirm", out[0].Code) catch { case proof.Unknown: case proof.Expired, proof.Spent, proof.TooManyAttempts, proof.Wrong, proof.Failed: return false } proof.RedeemCode(s, c, "confirm", out[0].Code) catch { case proof.Unknown, proof.Expired, proof.Spent, proof.TooManyAttempts, proof.Wrong, proof.Failed: return false } proof.RedeemCode(s, c, "confirm", out[0].Code) catch { case proof.Spent: return true case proof.Unknown, proof.Expired, proof.TooManyAttempts, proof.Wrong, proof.Failed: return false } false } })) == "" true }`,
	"proof/memory StepIsUsedOnce":       `StepIsUsedOnce(secret string, sec int64) { let s = New() let p = totp.Default() let m = time.NewManual(((sec%1000000000)+1000000000)%1000000000) with context { time.Clock: m } { let code = totp.Code(p, secret, m.Now()) proof.VerifyCode(s, p, "a", secret, code) catch { case proof.Wrong, proof.Spent, proof.Failed: return false } proof.VerifyCode(s, p, "b", secret, code) catch { case proof.Wrong, proof.Spent, proof.Failed: return false } m.Advance(30s) proof.VerifyCode(s, p, "a", secret, code) catch { case proof.Spent: return true case proof.Wrong, proof.Failed: return false } false } }`,
	"proof/memory RedeemHasOneWinner":   `RedeemHasOneWinner(id string, code string) { let s = New() let now = time.Unix(0, 0) let d = proof.DigestOf("p", code) s.Issue(id, d, now.Add(1m), 3) catch { case proof.Conflict, proof.Failed: return false } let ta = task.Go() { redeems(s, id, d, now) } let tb = task.Go() { redeems(s, id, d, now) } ta.Wait() != tb.Wait() }`,
	"proof/file FileIsMemory":           `FileIsMemory(steps []script, torn string) { let disk = fstest.NewMemory() let want = memory.New() let mut got = try Open(disk, disk, "proofs.log") let mut now = time.Unix(0, 0) for st in steps { match st { case issue as i: now = later(now, i.Wait) let id = "p\t\"\(((i.Pick%4)+4)%4)" if try issued(want, id, now) != try issued(got, id, now) { return false } case redeem as r: now = later(now, r.Wait) let id = "p\t\"\(((r.Pick%4)+4)%4)" let d = proof.DigestOf("p", if r.Right { "123456" } else { "654321" }) if try redeemed(want, id, d, now) != try redeemed(got, id, d, now) { return false } case claim as c: now = later(now, c.Wait) let key = "k\(((c.Pick%3)+3)%3)" if try claimed(want, key, now) != try claimed(got, key, now) { return false } case reopen: got = try Open(disk, disk, "proofs.log") } } let edge = time.Unix(time.UnixSeconds(now) + 1, 999) let e = proof.DigestOf("p", "edge") try want.Issue("edge", e, edge, 1) catch { case proof.Conflict: return false } try got.Issue("edge", e, edge, 1) catch { case proof.Conflict: return false } try disk.AppendFile("proofs.log", "red\t" + strings.ReplaceAll(torn, "\n", "")) let back = try Open(disk, disk, "proofs.log") let justBefore = time.Unix(time.UnixSeconds(now) + 1, 500) if try redeemed(want, "edge", e, justBefore) != try redeemed(back, "edge", e, justBefore) { return false } for i in 0..4 { let id = "p\t\"\(i)" let d = proof.DigestOf("p", "123456") if try redeemed(want, id, d, now) != try redeemed(back, id, d, now) { return false } } for k in 0..3 { if try claimed(want, "k\(k)", now) != try claimed(back, "k\(k)", now) { return false } } true }`,
	"proof/file OpenRefusesABrokenLine": `OpenRefusesABrokenLine(id string, line string) { let disk = fstest.NewMemory() let s = try Open(disk, disk, "proofs.log") try s.Issue(id, proof.DigestOf("p", "1"), time.Unix(60, 0), 3) catch { case proof.Conflict: return false } try disk.AppendFile("proofs.log", "x" + strings.ReplaceAll(line, "\n", "") + "\n") let _ = Open(disk, disk, "proofs.log") catch { case proof.Failed: return true } false }`,
	"proof/file LogHoldsNoCode":         `LogHoldsNoCode(code string) { let secret = "code:" + code let d = proof.DigestOf("p", secret) let disk = fstest.NewMemory() let s = try Open(disk, disk, "proofs.log") try s.Issue("id", d, time.Unix(60, 0), 3) catch { case proof.Conflict: return false } let log = try disk.ReadFile("proofs.log") !strings.Contains(log, secret) && strings.Contains(log, proof.DigestText(d)) }`,
}

// stdProofLawTestName is the probe's test of a ledger key: the last
// element of the package path, capitalized, then the law without its dot
// ("ratelimit/memory AgreesWithTheModel" is MemoryAgreesWithTheModel).
func stdProofLawTestName(key string) string {
	pkg, law, _ := strings.Cut(key, " ")
	return stdLawsTestName(pkg[strings.LastIndex(pkg, "/")+1:], law)
}

// TestStdProofLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. It builds Go because the laws run in the built test binary (ergo test), the only place a law runs; the file store's laws write through fstest.Memory, and RedeemHasOneWinner starts tasks, both in that binary.
func TestStdProofLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdproof"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdproof failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, stdProofLawStatements, stdProofLawTestName, stdProofLawsProbe, stdout))
}

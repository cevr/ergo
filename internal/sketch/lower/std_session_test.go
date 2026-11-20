package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdSessionLawsProbe restates the laws whose runner is TestStdSessionLawsRun
// as Ergo tests. A member law runs on a provider of each kind the probe
// draws; a law over a package's private helpers restates them.
const stdSessionLawsProbe = `import fs
import fstest
import rand
import session
import session/file
import session/memory
import sha256
import strings
import task
import time

// provider is a store of each kind, by pick: a memory store, or a file
// store over its own fstest.Memory disk; renewed after 10ms, idle 30ms,
// at most 100ms.
func provider(pick int) session.Store {
	let p = session.NewPolicy(10ms, 30ms, 100ms) catch {
	case session.BadPolicy:
		Unreachable("the policy is good")
	}
	if ((pick%2)+2)%2 == 0 {
		return memory.New(p)
	}
	let disk = fstest.NewMemory()
	file.Open(disk, disk, "sessions.log", p) catch {
	case session.Failed:
		Unreachable("an empty disk opens")
	}
}

func storeVerifyAfterEstablish(s session.Store, key string, subject string, revision int, sec int64) bool fails session.Failed {
	let d = session.DigestOf(session.TokenOf(key))
	let now = time.Unix(sec, 0)
	try s.Establish(d, subject, revision, now) catch {
	case session.Conflict:
		return true
	}
	try s.Verify(d, now) == session.Valid{Record: session.Record{Subject: subject, Revision: revision, Created: now, Renewed: now}}
}

test SessionStoreVerifyAfterEstablish(pick int, key string, subject string, revision int, sec int64) {
	let ok = storeVerifyAfterEstablish(provider(pick), key, subject, revision, sec) catch {
	case session.Failed:
		false
	}
	assert ok
}

func storeRevokedNeverVerifies(s session.Store, key string, subject string, sec int64) bool fails session.Failed {
	let d = session.DigestOf(session.TokenOf(key))
	let now = time.Unix(sec, 0)
	try s.Establish(d, subject, 0, now) catch {
	case session.Conflict:
		return true
	}
	if !try s.Revoke(d) || try s.Verify(d, now) != session.Invalid {
		return false
	}
	let _ = try s.Rotate(d, session.DigestOf(session.TokenOf(key + "'")), now) catch {
	case session.Conflict:
		return !try s.Revoke(d)
	}
	false
}

test SessionStoreRevokedNeverVerifies(pick int, key string, subject string, sec int64) {
	let ok = storeRevokedNeverVerifies(provider(pick), key, subject, sec) catch {
	case session.Failed:
		false
	}
	assert ok
}

func storeRotateMovesTheSession(s session.Store, key string, subject string, sec int64) bool fails session.Failed {
	let old = session.DigestOf(session.TokenOf(key))
	let new = session.DigestOf(session.TokenOf(key + "'"))
	let now = time.Unix(sec, 0)
	try s.Establish(old, subject, 0, now) catch {
	case session.Conflict:
		return true
	}
	let r = try s.Rotate(old, new, now) catch {
	case session.Conflict:
		return false
	}
	if r != (session.Record{Subject: subject, Revision: 0, Created: now, Renewed: now}) || try s.Verify(new, now) != (session.Valid{Record: r}) || try s.Verify(old, now) != session.Invalid {
		return false
	}
	let _ = try s.Rotate(old, session.DigestOf(session.TokenOf(key + "''")), now) catch {
	case session.Conflict:
		return true
	}
	false
}

test SessionStoreRotateMovesTheSession(pick int, key string, subject string, sec int64) {
	let ok = storeRotateMovesTheSession(provider(pick), key, subject, sec) catch {
	case session.Failed:
		false
	}
	assert ok
}

func storeRevisionEndsEverySession(s session.Store, key string, subject string, revision int, sec int64) bool fails session.Failed {
	let now = time.Unix(sec, 0)
	let rev = ((revision%1000)+1000)%1000
	let old = session.DigestOf(session.TokenOf(key + "old"))
	let late = session.DigestOf(session.TokenOf(key + "late"))
	let kept = session.DigestOf(session.TokenOf(key + "kept"))
	try s.Establish(old, subject, rev, now) catch {
	case session.Conflict:
		return true
	}
	try s.RevokeAll(subject, rev + 1)
	try s.Establish(late, subject, rev, now) catch {
	case session.Conflict:
		return true
	}
	try s.Establish(kept, subject, rev + 1, now) catch {
	case session.Conflict:
		return true
	}
	try s.Verify(old, now) == session.Invalid && try s.Verify(late, now) == session.Invalid && try s.Verify(kept, now) != session.Invalid
}

test SessionStoreRevisionEndsEverySession(pick int, key string, subject string, revision int, sec int64) {
	let ok = storeRevisionEndsEverySession(provider(pick), key, subject, revision, sec) catch {
	case session.Failed:
		false
	}
	assert ok
}

// session's law helpers, restated.
// ring is the keyring of the signed-token laws: key "k1" of 32 bytes made
// from seed, older keys from older, at generation.
func ring(id string, seed string, older []session.Key, generation int) session.Keyring {
	session.NewKeyring(session.Key{ID: id, Secret: sha256.Sum(seed)}, older, generation) catch {
	case session.BadKeyring:
		Unreachable("a ring of the laws is good")
	}
}

// expiredAt is whether token opens under k at at as session.Expired at at.
func expiredAt(k session.Keyring, token string, at time.Time) bool {
	let _ = session.Open(k, token, at) catch {
	case session.Expired as e:
		return e.At == at
	case session.Tampered, session.Retired:
		return false
	}
	false
}

func lawRenewalStaysUnderMaxAge(gaps []int) bool {
	let p = session.NewPolicy(10ms, 30ms, 100ms) catch {
	case session.BadPolicy:
		return false
	}
	let start = time.Unix(0, 0)
	let mut s = session.Empty()
	let mut d = session.DigestOf(session.TokenOf("0"))
	let (_, s0, _) = session.Apply(p, s, session.EstablishOp{Digest: d, Subject: "a", Revision: 0, Now: start})
	s = s0
	let mut now = start
	let mut renewed = start
	for i, g in gaps {
		now = now.Add(time.Times(1ms, ((g%35)+35)%35))
		let v = session.VerifyIn(p, s, d, now)
		let over = !now.Before(start.Add(100ms)) || !now.Before(renewed.Add(30ms))
		match v {
		case session.Invalid:
			if !over {
				return false
			}
			return true
		case session.Valid:
			if over || !now.Before(renewed.Add(10ms)) {
				return false
			}
		case session.Renew:
			if over || now.Before(renewed.Add(10ms)) {
				return false
			}
			let next = session.DigestOf(session.TokenOf("\(i + 1)"))
			let (o, s1, _) = session.Apply(p, s, session.RotateOp{Old: d, New: next, Now: now})
			s = s1
			if o != (session.Moved{Record: session.Record{Subject: "a", Revision: 0, Created: start, Renewed: now}}) {
				return false
			}
			d = next
			renewed = now
		}
	}
	true
}

test SessionRenewalStaysUnderMaxAge(gaps []int) {
	assert lawRenewalStaysUnderMaxAge(gaps)
}

func lawSignedTamperFails(subject string, sec int64, at int, by int) bool {
	let k = ring("k1", "seed", []session.Key{}, 0)
	let expires = time.Unix(((sec%1000000)+1000000)%1000000 + 10, 0)
	let token = session.Sign(k, subject, expires)
	let now = time.Unix(0, 0)
	let c = session.Open(k, token, now) catch {
	case session.Tampered, session.Expired, session.Retired:
		return false
	}
	if c != (session.Claims{Subject: subject, Expires: expires, Generation: 0}) || !expiredAt(k, token, expires) {
		return false
	}
	let i = ((at%len(token))+len(token))%len(token)
	let b = 1 + ((by%255)+255)%255
	let mut bs = strings.Bytes(token)
	bs[i] = byte((int(bs[i]) + b) % 256)
	let changed = strings.FromBytes(bs)
	let _ = session.Open(k, changed, now) catch {
	case session.Tampered:
		return true
	case session.Expired, session.Retired:
		return false
	}
	false
}

test SessionSignedTamperFails(subject string, sec int64, at int, by int) {
	assert lawSignedTamperFails(subject, sec, at, by)
}

func lawOldKeyStillVerifies(subject string) bool {
	let first = ring("k1", "one", []session.Key{}, 0)
	let token = session.Sign(first, subject, time.Unix(100, 0))
	let kept = ring("k2", "two", []session.Key{session.Key{ID: "k1", Secret: sha256.Sum("one")}}, 0)
	let dropped = ring("k2", "two", []session.Key{}, 0)
	let c = session.Open(kept, token, time.Unix(0, 0)) catch {
	case session.Tampered, session.Expired, session.Retired:
		return false
	}
	let _ = session.Open(dropped, token, time.Unix(0, 0)) catch {
	case session.Tampered:
		return c.Subject == subject
	case session.Expired, session.Retired:
		return false
	}
	false
}

test SessionOldKeyStillVerifies(subject string) {
	assert lawOldKeyStillVerifies(subject)
}

func lawGenerationBumpEndsTokens(subject string, gen int) bool {
	let g = ((gen%100)+100)%100
	let token = session.Sign(ring("k1", "one", []session.Key{}, g), subject, time.Unix(100, 0))
	let _ = session.Open(ring("k1", "one", []session.Key{}, g + 1), token, time.Unix(0, 0)) catch {
	case session.Retired as r:
		return r.Generation == g
	case session.Tampered, session.Expired:
		return false
	}
	false
}

test SessionGenerationBumpEndsTokens(subject string, gen int) {
	assert lawGenerationBumpEndsTokens(subject, gen)
}

// session/memory's law helpers, restated.
// wins is whether a rotation of old to new won.
func wins(s memory.Store, old session.Digest, new session.Digest, now time.Time) bool {
	let _ = s.Rotate(old, new, now) catch {
	case session.Conflict, session.Failed:
		return false
	}
	true
}

func lawRotateHasOneWinner(key string) bool {
	let p = session.NewPolicy(10ms, 30ms, 100ms) catch {
	case session.BadPolicy:
		return false
	}
	let s = memory.New(p)
	let now = time.Unix(0, 0)
	let old = session.DigestOf(session.TokenOf(key))
	let a = session.DigestOf(session.TokenOf(key + "a"))
	let b = session.DigestOf(session.TokenOf(key + "b"))
	s.Establish(old, "subject", 0, now) catch {
	case session.Conflict, session.Failed:
		return false
	}
	let ta = task.Go() { wins(s, old, a, now) }
	let tb = task.Go() { wins(s, old, b, now) }
	let wa = ta.Wait()
	let wb = tb.Wait()
	let va = s.Verify(a, now) catch {
	case session.Failed:
		session.Invalid
	}
	let vb = s.Verify(b, now) catch {
	case session.Failed:
		session.Invalid
	}
	wa != wb && (va != session.Invalid) == wa && (vb != session.Invalid) == wb
}

test MemoryRotateHasOneWinner(key string) {
	assert lawRotateHasOneWinner(key)
}

func lawStartChecksValid(seed uint64, subject string) bool {
	let p = session.NewPolicy(10ms, 30ms, 100ms) catch {
	case session.BadPolicy:
		return false
	}
	let s = memory.New(p)
	with context { rand.Secure: rand.NewSeededSecure(seed), time.Clock: time.NewManual(0) } {
		let t = session.Start(s, subject, 0) catch {
		case session.Conflict, session.Failed:
			return false
		}
		let now = time.Clock.Now()
		let want = session.Valid{Record: session.Record{Subject: subject, Revision: 0, Created: now, Renewed: now}}
		let checked = session.Check(s, t) catch {
		case session.Failed:
			return false
		}
		let byDigest = session.Check(s, session.TokenOf(session.DigestText(session.DigestOf(t)))) catch {
		case session.Failed:
			return false
		}
		let ended = session.End(s, t) catch {
		case session.Failed:
			return false
		}
		let after = session.Check(s, t) catch {
		case session.Failed:
			return false
		}
		checked == want && byDigest == session.Invalid && ended && after == session.Invalid
	}
}

test MemoryStartChecksValid(seed uint64, subject string) {
	assert lawStartChecksValid(seed, subject)
}

// session/file's law helpers, restated.
// script is one step of a FileIsMemory script: wait Wait milliseconds,
// then establish, verify, rotate or revoke the Pick-th digest handed out,
// revoke every session of the subject below a revision, or open the file
// store again.
type script = establish{Subject int, Revision int, Wait int} | verify{Pick int, Wait int} | rotate{Pick int, Wait int} | revoke{Pick int} | revokeAll{Subject int, Revision int} | reopen{}

// later is t plus ms milliseconds, with no monotonic reading, as a time
// read back from the log is: == of two records compares it.
func later(t time.Time, ms int) time.Time { time.Unix(time.UnixSeconds(t), time.Nanosecond(t) + int64(ms)*1000000) }

// established is whether s kept the session, or found it a Conflict.
func established(s session.Store, d session.Digest, subject string, revision int, now time.Time) bool fails session.Failed {
	try s.Establish(d, subject, revision, now) catch {
	case session.Conflict:
		return false
	}
	true
}

// rotated is the record a rotation gave, or None at a Conflict.
func rotated(s session.Store, old session.Digest, new session.Digest, now time.Time) Option[session.Record] fails session.Failed {
	let r = try s.Rotate(old, new, now) catch {
	case session.Conflict:
		return None
	}
	Some[session.Record]{Value: r}
}

func lawFileIsMemory(steps []script, torn string) bool fails session.Failed | fs.NotFound | fs.Denied | fs.Failed {
	let p = session.NewPolicy(10ms, 30ms, 100ms) catch {
	case session.BadPolicy:
		return false
	}
	let disk = fstest.NewMemory()
	let want = memory.New(p)
	let mut got = try file.Open(disk, disk, "sessions.log", p)
	let mut now = time.Unix(0, 0)
	let mut n = 0
	let mut digests = []session.Digest{session.DigestOf(session.TokenOf("none"))}
	for st in steps {
		match st {
		case establish as e:
			now = later(now, ((e.Wait%20)+20)%20)
			n = n + 1
			let d = session.DigestOf(session.TokenOf("t\(n)"))
			let subject = "s\t\"\(((e.Subject%3)+3)%3)"
			let rev = ((e.Revision%3)+3)%3
			if try established(want, d, subject, rev, now) != try established(got, d, subject, rev, now) {
				return false
			}
			digests = digests.Append(d)
		case verify as v:
			now = later(now, ((v.Wait%20)+20)%20)
			let d = digests[((v.Pick%len(digests))+len(digests))%len(digests)]
			if try want.Verify(d, now) != try got.Verify(d, now) {
				return false
			}
		case rotate as r:
			now = later(now, ((r.Wait%20)+20)%20)
			n = n + 1
			let d = digests[((r.Pick%len(digests))+len(digests))%len(digests)]
			let next = session.DigestOf(session.TokenOf("t\(n)"))
			if try rotated(want, d, next, now) != try rotated(got, d, next, now) {
				return false
			}
			digests = digests.Append(next)
		case revoke as r:
			let d = digests[((r.Pick%len(digests))+len(digests))%len(digests)]
			if try want.Revoke(d) != try got.Revoke(d) {
				return false
			}
		case revokeAll as a:
			let subject = "s\t\"\(((a.Subject%3)+3)%3)"
			let rev = ((a.Revision%4)+4)%4
			try want.RevokeAll(subject, rev)
			try got.RevokeAll(subject, rev)
		case reopen:
			got = try file.Open(disk, disk, "sessions.log", p)
		}
	}
	try disk.AppendFile("sessions.log", "est\t" + strings.ReplaceAll(torn, "\n", ""))
	let back = try file.Open(disk, disk, "sessions.log", p)
	for d in digests {
		if try want.Verify(d, now) != try back.Verify(d, now) {
			return false
		}
	}
	true
}

test FileFileIsMemory(steps []script, torn string) {
	let ok = lawFileIsMemory(steps, torn) catch {
	case session.Failed, fs.NotFound, fs.Denied, fs.Failed:
		false
	}
	assert ok
}

func lawOpenRefusesABrokenLine(key string, line string) bool fails session.Failed | fs.NotFound | fs.Denied | fs.Failed {
	let p = session.NewPolicy(10ms, 30ms, 100ms) catch {
	case session.BadPolicy:
		return false
	}
	let disk = fstest.NewMemory()
	let s = try file.Open(disk, disk, "sessions.log", p)
	try s.Establish(session.DigestOf(session.TokenOf(key)), "a", 0, time.Unix(0, 0)) catch {
	case session.Conflict:
		return false
	}
	try disk.AppendFile("sessions.log", "x" + strings.ReplaceAll(line, "\n", "") + "\n")
	let _ = file.Open(disk, disk, "sessions.log", p) catch {
	case session.Failed:
		return true
	}
	false
}

test FileOpenRefusesABrokenLine(key string, line string) {
	let ok = lawOpenRefusesABrokenLine(key, line) catch {
	case session.Failed, fs.NotFound, fs.Denied, fs.Failed:
		false
	}
	assert ok
}

func lawLogHoldsNoToken(key string) bool fails session.Failed | fs.NotFound | fs.Denied | fs.Failed {
	let p = session.NewPolicy(10ms, 30ms, 100ms) catch {
	case session.BadPolicy:
		return false
	}
	let token = "token:" + key
	let d = session.DigestOf(session.TokenOf(token))
	let disk = fstest.NewMemory()
	let s = try file.Open(disk, disk, "sessions.log", p)
	try s.Establish(d, "a", 0, time.Unix(0, 0)) catch {
	case session.Conflict:
		return false
	}
	let log = try disk.ReadFile("sessions.log")
	!strings.Contains(log, token) && strings.Contains(log, session.DigestText(d))
}

test FileLogHoldsNoToken(key string) {
	let ok = lawLogHoldsNoToken(key) catch {
	case session.Failed, fs.NotFound, fs.Denied, fs.Failed:
		false
	}
	assert ok
}

`

// stdSessionLawStatements is the statement of each law the probe restates,
// by ledger key; lawledger.Run holds it equal to the ledger.
var stdSessionLawStatements = map[string]string{
	"session Store.VerifyAfterEstablish":     `VerifyAfterEstablish(key string, subject string, revision int, sec int64) { let d = DigestOf(TokenOf(key)) let now = time.Unix(sec, 0) try Establish(d, subject, revision, now) catch { case Conflict: return true } try Verify(d, now) == Valid{Record: Record{Subject: subject, Revision: revision, Created: now, Renewed: now}} }`,
	"session Store.RevokedNeverVerifies":     `RevokedNeverVerifies(key string, subject string, sec int64) { let d = DigestOf(TokenOf(key)) let now = time.Unix(sec, 0) try Establish(d, subject, 0, now) catch { case Conflict: return true } if !try Revoke(d) || try Verify(d, now) != Invalid { return false } let _ = try Rotate(d, DigestOf(TokenOf(key + "'")), now) catch { case Conflict: return !try Revoke(d) } false }`,
	"session Store.RotateMovesTheSession":    `RotateMovesTheSession(key string, subject string, sec int64) { let old = DigestOf(TokenOf(key)) let new = DigestOf(TokenOf(key + "'")) let now = time.Unix(sec, 0) try Establish(old, subject, 0, now) catch { case Conflict: return true } let r = try Rotate(old, new, now) catch { case Conflict: return false } if r != (Record{Subject: subject, Revision: 0, Created: now, Renewed: now}) || try Verify(new, now) != (Valid{Record: r}) || try Verify(old, now) != Invalid { return false } let _ = try Rotate(old, DigestOf(TokenOf(key + "''")), now) catch { case Conflict: return true } false }`,
	"session Store.RevisionEndsEverySession": `RevisionEndsEverySession(key string, subject string, revision int, sec int64) { let now = time.Unix(sec, 0) let rev = ((revision%1000)+1000)%1000 let old = DigestOf(TokenOf(key + "old")) let late = DigestOf(TokenOf(key + "late")) let kept = DigestOf(TokenOf(key + "kept")) try Establish(old, subject, rev, now) catch { case Conflict: return true } try RevokeAll(subject, rev + 1) try Establish(late, subject, rev, now) catch { case Conflict: return true } try Establish(kept, subject, rev + 1, now) catch { case Conflict: return true } try Verify(old, now) == Invalid && try Verify(late, now) == Invalid && try Verify(kept, now) != Invalid }`,
	"session RenewalStaysUnderMaxAge":        `RenewalStaysUnderMaxAge(gaps []int) { let p = NewPolicy(10ms, 30ms, 100ms) catch { case BadPolicy: return false } let start = time.Unix(0, 0) let mut s = Empty() let mut d = DigestOf(TokenOf("0")) let (_, s0, _) = Apply(p, s, EstablishOp{Digest: d, Subject: "a", Revision: 0, Now: start}) s = s0 let mut now = start let mut renewed = start for i, g in gaps { now = now.Add(time.Times(1ms, ((g%35)+35)%35)) let v = VerifyIn(p, s, d, now) let over = !now.Before(start.Add(100ms)) || !now.Before(renewed.Add(30ms)) match v { case Invalid: if !over { return false } return true case Valid: if over || !now.Before(renewed.Add(10ms)) { return false } case Renew: if over || now.Before(renewed.Add(10ms)) { return false } let next = DigestOf(TokenOf("\(i + 1)")) let (o, s1, _) = Apply(p, s, RotateOp{Old: d, New: next, Now: now}) s = s1 if o != (Moved{Record: Record{Subject: "a", Revision: 0, Created: start, Renewed: now}}) { return false } d = next renewed = now } } true }`,
	"session SignedTamperFails":              `SignedTamperFails(subject string, sec int64, at int, by int) { let k = ring("k1", "seed", []Key{}, 0) let expires = time.Unix(((sec%1000000)+1000000)%1000000 + 10, 0) let token = Sign(k, subject, expires) let now = time.Unix(0, 0) let c = Open(k, token, now) catch { case Tampered, Expired, Retired: return false } if c != (Claims{Subject: subject, Expires: expires, Generation: 0}) || !expiredAt(k, token, expires) { return false } let i = ((at%len(token))+len(token))%len(token) let b = 1 + ((by%255)+255)%255 let mut bs = strings.Bytes(token) bs[i] = byte((int(bs[i]) + b) % 256) let changed = strings.FromBytes(bs) let _ = Open(k, changed, now) catch { case Tampered: return true case Expired, Retired: return false } false }`,
	"session OldKeyStillVerifies":            `OldKeyStillVerifies(subject string) { let first = ring("k1", "one", []Key{}, 0) let token = Sign(first, subject, time.Unix(100, 0)) let kept = ring("k2", "two", []Key{Key{ID: "k1", Secret: sha256.Sum("one")}}, 0) let dropped = ring("k2", "two", []Key{}, 0) let c = Open(kept, token, time.Unix(0, 0)) catch { case Tampered, Expired, Retired: return false } let _ = Open(dropped, token, time.Unix(0, 0)) catch { case Tampered: return c.Subject == subject case Expired, Retired: return false } false }`,
	"session GenerationBumpEndsTokens":       `GenerationBumpEndsTokens(subject string, gen int) { let g = ((gen%100)+100)%100 let token = Sign(ring("k1", "one", []Key{}, g), subject, time.Unix(100, 0)) let _ = Open(ring("k1", "one", []Key{}, g + 1), token, time.Unix(0, 0)) catch { case Retired as r: return r.Generation == g case Tampered, Expired: return false } false }`,
	"session/memory RotateHasOneWinner":      `RotateHasOneWinner(key string) { let p = session.NewPolicy(10ms, 30ms, 100ms) catch { case session.BadPolicy: return false } let s = New(p) let now = time.Unix(0, 0) let old = session.DigestOf(session.TokenOf(key)) let a = session.DigestOf(session.TokenOf(key + "a")) let b = session.DigestOf(session.TokenOf(key + "b")) s.Establish(old, "subject", 0, now) catch { case session.Conflict, session.Failed: return false } let ta = task.Go() { wins(s, old, a, now) } let tb = task.Go() { wins(s, old, b, now) } let wa = ta.Wait() let wb = tb.Wait() let va = s.Verify(a, now) catch { case session.Failed: session.Invalid } let vb = s.Verify(b, now) catch { case session.Failed: session.Invalid } wa != wb && (va != session.Invalid) == wa && (vb != session.Invalid) == wb }`,
	"session/memory StartChecksValid":        `StartChecksValid(seed uint64, subject string) { let p = session.NewPolicy(10ms, 30ms, 100ms) catch { case session.BadPolicy: return false } let s = New(p) with context { rand.Secure: rand.NewSeededSecure(seed), time.Clock: time.NewManual(0) } { let t = session.Start(s, subject, 0) catch { case session.Conflict, session.Failed: return false } let now = time.Clock.Now() let want = session.Valid{Record: session.Record{Subject: subject, Revision: 0, Created: now, Renewed: now}} let checked = session.Check(s, t) catch { case session.Failed: return false } let byDigest = session.Check(s, session.TokenOf(session.DigestText(session.DigestOf(t)))) catch { case session.Failed: return false } let ended = session.End(s, t) catch { case session.Failed: return false } let after = session.Check(s, t) catch { case session.Failed: return false } checked == want && byDigest == session.Invalid && ended && after == session.Invalid } }`,
	"session/file FileIsMemory":              `FileIsMemory(steps []script, torn string) { let p = session.NewPolicy(10ms, 30ms, 100ms) catch { case session.BadPolicy: return false } let disk = fstest.NewMemory() let want = memory.New(p) let mut got = try Open(disk, disk, "sessions.log", p) let mut now = time.Unix(0, 0) let mut n = 0 let mut digests = []session.Digest{session.DigestOf(session.TokenOf("none"))} for st in steps { match st { case establish as e: now = later(now, ((e.Wait%20)+20)%20) n = n + 1 let d = session.DigestOf(session.TokenOf("t\(n)")) let subject = "s\t\"\(((e.Subject%3)+3)%3)" let rev = ((e.Revision%3)+3)%3 if try established(want, d, subject, rev, now) != try established(got, d, subject, rev, now) { return false } digests = digests.Append(d) case verify as v: now = later(now, ((v.Wait%20)+20)%20) let d = digests[((v.Pick%len(digests))+len(digests))%len(digests)] if try want.Verify(d, now) != try got.Verify(d, now) { return false } case rotate as r: now = later(now, ((r.Wait%20)+20)%20) n = n + 1 let d = digests[((r.Pick%len(digests))+len(digests))%len(digests)] let next = session.DigestOf(session.TokenOf("t\(n)")) if try rotated(want, d, next, now) != try rotated(got, d, next, now) { return false } digests = digests.Append(next) case revoke as r: let d = digests[((r.Pick%len(digests))+len(digests))%len(digests)] if try want.Revoke(d) != try got.Revoke(d) { return false } case revokeAll as a: let subject = "s\t\"\(((a.Subject%3)+3)%3)" let rev = ((a.Revision%4)+4)%4 try want.RevokeAll(subject, rev) try got.RevokeAll(subject, rev) case reopen: got = try Open(disk, disk, "sessions.log", p) } } try disk.AppendFile("sessions.log", "est\t" + strings.ReplaceAll(torn, "\n", "")) let back = try Open(disk, disk, "sessions.log", p) for d in digests { if try want.Verify(d, now) != try back.Verify(d, now) { return false } } true }`,
	"session/file OpenRefusesABrokenLine":    `OpenRefusesABrokenLine(key string, line string) { let p = session.NewPolicy(10ms, 30ms, 100ms) catch { case session.BadPolicy: return false } let disk = fstest.NewMemory() let s = try Open(disk, disk, "sessions.log", p) try s.Establish(session.DigestOf(session.TokenOf(key)), "a", 0, time.Unix(0, 0)) catch { case session.Conflict: return false } try disk.AppendFile("sessions.log", "x" + strings.ReplaceAll(line, "\n", "") + "\n") let _ = Open(disk, disk, "sessions.log", p) catch { case session.Failed: return true } false }`,
	"session/file LogHoldsNoToken":           `LogHoldsNoToken(key string) { let p = session.NewPolicy(10ms, 30ms, 100ms) catch { case session.BadPolicy: return false } let token = "token:" + key let d = session.DigestOf(session.TokenOf(token)) let disk = fstest.NewMemory() let s = try Open(disk, disk, "sessions.log", p) try s.Establish(d, "a", 0, time.Unix(0, 0)) catch { case session.Conflict: return false } let log = try disk.ReadFile("sessions.log") !strings.Contains(log, token) && strings.Contains(log, session.DigestText(d)) }`,
}

// stdSessionLawTestName is the probe's test of a ledger key: the last
// element of the package path, capitalized, then the law without its dot
// ("ratelimit/memory AgreesWithTheModel" is MemoryAgreesWithTheModel).
func stdSessionLawTestName(key string) string {
	pkg, law, _ := strings.Cut(key, " ")
	return stdLawsTestName(pkg[strings.LastIndex(pkg, "/")+1:], law)
}

// TestStdSessionLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. It builds Go because the laws run in the built test binary (ergo test), the only place a law runs; the file store's laws write through fstest.Memory, and RotateHasOneWinner starts tasks, both in that binary.
func TestStdSessionLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdsession"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdsession failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, stdSessionLawStatements, stdSessionLawTestName, stdSessionLawsProbe, stdout))
}

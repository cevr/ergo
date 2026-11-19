package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdJobsLawsProbe restates the laws whose runner is TestStdJobsLawsRun
// as Ergo tests. A member law runs on a provider of each kind the probe
// draws; a law over a package's private helpers restates them.
const stdJobsLawsProbe = `import fs
import fstest
import jobs
import jobs/file
import jobs/memory
import schedule
import strings
import time

// provider is a queue of each kind, by pick: a memory queue, or a file
// queue over its own fstest.Memory disk; a lease of 10ms, two retries.
func provider(pick int) jobs.Queue[string] {
	let p = jobs.Policy{Retry: schedule.Recurs(2), Lease: 10ms}
	if ((pick%2)+2)%2 == 0 {
		return memory.New[string](p) catch {
		case jobs.BadPolicy:
			Unreachable("the lease is above zero")
		}
	}
	let disk = fstest.NewMemory()
	file.Open(disk, disk, "jobs.log", p, file.Text) catch {
	case jobs.Failed:
		Unreachable("an empty disk opens")
	}
}

func enqueueAddsOneWaiting(q jobs.Queue[string], job string, sec int64) bool fails jobs.Failed {
	let before = try q.Count()
	let _ = try q.EnqueueAt(job, time.Unix(sec, 0))
	let after = try q.Count()
	after == jobs.Counts{Waiting: before.Waiting + 1, Leased: before.Leased, Done: before.Done, Dead: before.Dead}
}

test JobsQueueEnqueueAddsOneWaiting(pick int, job string, sec int64) {
	let ok = enqueueAddsOneWaiting(provider(pick), job, sec) catch {
	case jobs.Failed:
		false
	}
	assert ok
}

func oneLeasePerJob(q jobs.Queue[string], job string, sec int64) bool fails jobs.Failed {
	let now = time.Unix(sec, 0)
	let id = try q.EnqueueAt(job, now)
	let before = try q.Count()
	match try q.ReserveAt(now) {
	case Some[jobs.Lease[string]] as l:
		if l.Value.Ticket.ID != id {
			return true
		}
		let held = try q.Count()
		if held.Leased != before.Leased + 1 || held.Waiting != before.Waiting - 1 {
			return false
		}
		try q.AckAt(l.Value.Ticket, now) catch {
		case jobs.Unknown:
			return false
		}
		let done = try q.Count()
		if done.Done != held.Done + 1 || done.Leased != held.Leased - 1 {
			return false
		}
		try q.AckAt(l.Value.Ticket, now) catch {
		case jobs.Unknown:
			return true
		}
		false
	case None:
		false
	}
}

test JobsQueueOneLeasePerJob(pick int, job string, sec int64) {
	let ok = oneLeasePerJob(provider(pick), job, sec) catch {
	case jobs.Failed:
		false
	}
	assert ok
}

func noTicketIsUnknown(q jobs.Queue[string], sec int64) bool fails jobs.Failed {
	let t = jobs.Ticket{ID: -1, Attempt: 1}
	let now = time.Unix(sec, 0)
	try q.AckAt(t, now) catch {
	case jobs.Unknown as u:
		if u.Ticket != t {
			return false
		}
		try q.FailAt(t, "no job", now) catch {
		case jobs.Unknown:
			return true
		}
		return false
	}
	false
}

test JobsQueueNoTicketIsUnknown(pick int, sec int64) {
	let ok = noTicketIsUnknown(provider(pick), sec) catch {
	case jobs.Failed:
		false
	}
	assert ok
}

// jobs' law helpers, restated.
// jstep is one jstep of a NoneLost script: wait Wait milliseconds, then
// enqueue a job, reserve, or ack or fail the Pick-th lease handed out.
type jstep = enqueueStep{Wait int} | reserveStep{Wait int} | ackStep{Pick int, Wait int} | failStep{Pick int, Wait int}

// msOf is a jstep's wait, 0 to 19 milliseconds.
func msOf(n int) duration { time.Times(1ms, ((n%20)+20)%20) }


func lawNoneLost(steps []jstep) bool {
	let p = jobs.Policy{Retry: schedule.Recurs(2), Lease: 10ms}
	let mut s = jobs.Empty[int]()
	let mut now = time.Unix(0, 0)
	let mut enqueued = 0
	let mut tickets = []jobs.Ticket{}
	for st in steps {
		let op jobs.Op[int] = match st {
		case enqueueStep as e:
			now = now.Add(msOf(e.Wait))
			jobs.Enqueue[int]{Job: enqueued * 7, At: now}
		case reserveStep as r:
			now = now.Add(msOf(r.Wait))
			jobs.Reserve{Now: now}
		case ackStep as a:
			now = now.Add(msOf(a.Wait))
			let t = if len(tickets) == 0 { jobs.Ticket{ID: 0, Attempt: 0} } else { tickets[((a.Pick%len(tickets))+len(tickets))%len(tickets)] }
			jobs.Ack{Ticket: t, Now: now}
		case failStep as f:
			now = now.Add(msOf(f.Wait))
			let t = if len(tickets) == 0 { jobs.Ticket{ID: 0, Attempt: 0} } else { tickets[((f.Pick%len(tickets))+len(tickets))%len(tickets)] }
			jobs.Fail{Ticket: t, Why: "failed", Now: now}
		}
		let (o, next, _) = jobs.Apply(p, s, op)
		s = next
		match o {
		case jobs.Enqueued as e:
			if e.ID != enqueued + 1 {
				return false
			}
			enqueued = enqueued + 1
		case jobs.Reserved[int] as r:
			match r.Lease {
			case Some[jobs.Lease[int]] as l:
				if l.Value.Job != (l.Value.Ticket.ID - 1) * 7 {
					return false
				}
				for t in tickets {
					if t.ID == l.Value.Ticket.ID && t.Attempt >= l.Value.Ticket.Attempt {
						return false
					}
				}
				tickets = tickets.Append(l.Value.Ticket)
			case None:
			}
		case jobs.Settled, jobs.NotLeased:
		}
		let c = jobs.CountOf(s)
		if c.Waiting + c.Leased + c.Done + c.Dead != enqueued || c.Waiting < 0 || c.Leased < 0 {
			return false
		}
	}
	true
}

test JobsNoneLost(steps []jstep) {
	assert lawNoneLost(steps)
}

func lawAttemptsFollowTheSchedule(base int, retries int, limit int) bool {
	let n = ((retries%5)+5)%5
	let backoff = schedule.Exponential(time.Times(1ms, 1+((base%10)+10)%10), 2.0, 50ms)
	let p = jobs.Policy{Retry: schedule.UpTo(schedule.And(backoff, schedule.Recurs(n)), time.Times(1ms, ((limit%80)+80)%80)), Lease: 1s}
	let start = time.Unix(1000, 0)
	let (_, s0, _) = jobs.Apply(p, jobs.Empty[string](), jobs.Enqueue[string]{Job: "job", At: start})
	let mut s = s0
	let mut at = start
	for attempt in 1..n+2 {
		let (o, s1, _) = jobs.Apply(p, s, jobs.Reserve{Now: at})
		s = s1
		let t = match o {
		case jobs.Reserved[string] as r:
			match r.Lease {
			case Some[jobs.Lease[string]] as l:
				l.Value.Ticket
			case None:
				return false
			}
		case _:
			return false
		}
		if t.Attempt != attempt {
			return false
		}
		let failedAt = at.Add(1ms)
		let (_, s2, _) = jobs.Apply(p, s, jobs.Fail{Ticket: t, Why: "try \(attempt)", Now: failedAt})
		s = s2
		match schedule.Decide(p.Retry, attempt, failedAt.Sub(start)) {
		case schedule.Continue as c:
			at = failedAt.Add(c.After)
			let (early, s3, _) = jobs.Apply(p, s, jobs.Reserve{Now: at.Add(-1ns)})
			s = s3
			if early != (jobs.Reserved[string]{Lease: None}) {
				return false
			}
		case schedule.Stop:
			let dead = jobs.DeadOf(s)
			return len(dead) == 1 && dead[0].Attempts == attempt && dead[0].Why == "try \(attempt)" && jobs.CountOf(s) == jobs.Counts{Waiting: 0, Leased: 0, Done: 0, Dead: 1}
		}
	}
	false
}

test JobsAttemptsFollowTheSchedule(base int, retries int, limit int) {
	assert lawAttemptsFollowTheSchedule(base, retries, limit)
}

func lawLeaseEndIsAFailure(sec int64, ms int) bool {
	let p = jobs.Policy{Retry: schedule.Forever(), Lease: time.Times(1ms, 1+((ms%100)+100)%100)}
	let now = time.Unix(sec%1000000, 0)
	let (_, s0, _) = jobs.Apply(p, jobs.Empty[int](), jobs.Enqueue[int]{Job: 1, At: now})
	let (o1, s1, _) = jobs.Apply(p, s0, jobs.Reserve{Now: now})
	let first = match o1 {
	case jobs.Reserved[int] as r:
		match r.Lease {
		case Some[jobs.Lease[int]] as l:
			l.Value
		case None:
			return false
		}
	case _:
		return false
	}
	let (late, _, _) = jobs.Apply(p, s1, jobs.Ack{Ticket: first.Ticket, Now: first.Until})
	if first.Until != now.Add(p.Lease) || late != (jobs.NotLeased{Ticket: first.Ticket}) {
		return false
	}
	let (o2, s2, _) = jobs.Apply(p, s1, jobs.Reserve{Now: first.Until})
	let (o3, _, _) = jobs.Apply(p, s2, jobs.Ack{Ticket: first.Ticket, Now: first.Until})
	match o2 {
	case jobs.Reserved[int] as r:
		match r.Lease {
		case Some[jobs.Lease[int]] as l:
			l.Value.Ticket == jobs.Ticket{ID: first.Ticket.ID, Attempt: 2} && o3 == jobs.NotLeased{Ticket: first.Ticket}
		case None:
			false
		}
	case _:
		false
	}
}

test JobsLeaseEndIsAFailure(sec int64, ms int) {
	assert lawLeaseEndIsAFailure(sec, ms)
}

func lawEarliestFirst(offsets []int) bool {
	let p = jobs.Policy{Retry: schedule.Forever(), Lease: 1s}
	let start = time.Unix(0, 0)
	let mut s = jobs.Empty[int]()
	for i, o in offsets {
		let (_, next, _) = jobs.Apply(p, s, jobs.Enqueue[int]{Job: i, At: start.Add(time.Times(1ms, ((o%5)+5)%5))})
		s = next
	}
	let mut prevAt = start
	let mut prevID = 0
	for _ in offsets {
		let (o, next, _) = jobs.Apply(p, s, jobs.Reserve{Now: start.Add(10ms)})
		s = next
		let l = match o {
		case jobs.Reserved[int] as r:
			match r.Lease {
			case Some[jobs.Lease[int]] as found:
				found.Value
			case None:
				return false
			}
		case _:
			return false
		}
		let at = start.Add(time.Times(1ms, ((offsets[l.Job]%5)+5)%5))
		let c = at.Compare(prevAt)
		if c < 0 || (c == 0 && l.Ticket.ID < prevID) || l.Ticket.ID != l.Job + 1 {
			return false
		}
		prevAt = at
		prevID = l.Ticket.ID
	}
	true
}

test JobsEarliestFirst(offsets []int) {
	assert lawEarliestFirst(offsets)
}

// jobs/file's law helpers, restated.
// script is one step of a FileIsMemory script: wait Wait milliseconds,
// then enqueue, reserve, ack or fail the Pick-th lease handed out, or
// open the file queue again.
type script = enqueueAt{Wait int} | reserveAt{Wait int} | ackAt{Pick int, Wait int} | failAt{Pick int, Wait int} | reopen{}

// acked is whether q acked t at now, or found it Unknown.
func acked(q jobs.Queue[string], t jobs.Ticket, now time.Time) bool fails jobs.Failed {
	try q.AckAt(t, now) catch {
	case jobs.Unknown:
		return false
	}
	true
}

// failed is whether q failed t at now, or found it Unknown.
func failed(q jobs.Queue[string], t jobs.Ticket, now time.Time) bool fails jobs.Failed {
	try q.FailAt(t, "no", now) catch {
	case jobs.Unknown:
		return false
	}
	true
}


func lawFileIsMemory(steps []script, torn string) bool fails jobs.Failed | jobs.BadPolicy | fs.NotFound | fs.Denied | fs.Failed {
	let p = jobs.Policy{Retry: schedule.Recurs(2), Lease: 10ms}
	let disk = fstest.NewMemory()
	let want = try memory.New[string](p)
	let mut got = try file.Open(disk, disk, "jobs.log", p, file.Text)
	let mut now = time.Unix(0, 0)
	let mut n = 0
	let mut tickets = []jobs.Ticket{}
	for st in steps {
		match st {
		case enqueueAt as e:
			now = now.Add(time.Times(1ms, ((e.Wait%20)+20)%20))
			n = n + 1
			if try want.EnqueueAt("job \(n)\t\n", now) != try got.EnqueueAt("job \(n)\t\n", now) {
				return false
			}
		case reserveAt as r:
			now = now.Add(time.Times(1ms, ((r.Wait%20)+20)%20))
			let a = try want.ReserveAt(now)
			if a != try got.ReserveAt(now) {
				return false
			}
			match a {
			case Some[jobs.Lease[string]] as l:
				tickets = tickets.Append(l.Value.Ticket)
			case None:
			}
		case ackAt as a:
			now = now.Add(time.Times(1ms, ((a.Wait%20)+20)%20))
			let t = if len(tickets) == 0 { jobs.Ticket{ID: 0, Attempt: 0} } else { tickets[((a.Pick%len(tickets))+len(tickets))%len(tickets)] }
			if try acked(want, t, now) != try acked(got, t, now) {
				return false
			}
		case failAt as f:
			now = now.Add(time.Times(1ms, ((f.Wait%20)+20)%20))
			let t = if len(tickets) == 0 { jobs.Ticket{ID: 0, Attempt: 0} } else { tickets[((f.Pick%len(tickets))+len(tickets))%len(tickets)] }
			if try failed(want, t, now) != try failed(got, t, now) {
				return false
			}
		case reopen:
			got = try file.Open(disk, disk, "jobs.log", p, file.Text)
		}
	}
	if try got.Count() != try want.Count() || try got.DeadLetters() != try want.DeadLetters() {
		return false
	}
	try disk.AppendFile("jobs.log", "enq\t" + strings.ReplaceAll(torn, "\n", ""))
	let back = try file.Open(disk, disk, "jobs.log", p, file.Text)
	if try back.Count() != try want.Count() || try back.DeadLetters() != try want.DeadLetters() {
		return false
	}
	let _ = try back.EnqueueAt(torn, now)
	let _ = try want.EnqueueAt(torn, now)
	let again = try file.Open(disk, disk, "jobs.log", p, file.Text)
	try again.Count() == try want.Count() && try again.ReserveAt(now) == try want.ReserveAt(now)
}

test FileFileIsMemory(steps []script, torn string) {
	let ok = lawFileIsMemory(steps, torn) catch {
	case jobs.Failed, jobs.BadPolicy, fs.NotFound, fs.Denied, fs.Failed:
		false
	}
	assert ok
}

func lawOpenRefusesABrokenLine(job string, line string) bool fails jobs.Failed | jobs.BadPolicy | fs.NotFound | fs.Denied | fs.Failed {
	let p = jobs.Policy{Retry: schedule.Forever(), Lease: 1s}
	let disk = fstest.NewMemory()
	let q = try file.Open(disk, disk, "jobs.log", p, file.Text)
	let _ = try q.EnqueueAt(job, time.Unix(0, 0))
	try disk.AppendFile("jobs.log", "x" + strings.ReplaceAll(line, "\n", "") + "\n")
	let _ = file.Open(disk, disk, "jobs.log", p, file.Text) catch {
	case jobs.Failed:
		return true
	}
	false
}

test FileOpenRefusesABrokenLine(job string, line string) {
	let ok = lawOpenRefusesABrokenLine(job, line) catch {
	case jobs.Failed, jobs.BadPolicy, fs.NotFound, fs.Denied, fs.Failed:
		false
	}
	assert ok
}

`

// stdJobsLawStatements is the statement of each law the probe restates,
// by ledger key; lawledger.Run holds it equal to the ledger.
var stdJobsLawStatements = map[string]string{
	"jobs Queue.EnqueueAddsOneWaiting": `EnqueueAddsOneWaiting(job J, sec int64) { let before = try Count() let _ = try EnqueueAt(job, time.Unix(sec, 0)) let after = try Count() after == Counts{Waiting: before.Waiting + 1, Leased: before.Leased, Done: before.Done, Dead: before.Dead} }`,
	"jobs Queue.OneLeasePerJob":        `OneLeasePerJob(job J, sec int64) { let now = time.Unix(sec, 0) let id = try EnqueueAt(job, now) let before = try Count() match try ReserveAt(now) { case Some[Lease[J]] as l: if l.Value.Ticket.ID != id { return true } let held = try Count() if held.Leased != before.Leased + 1 || held.Waiting != before.Waiting - 1 { return false } try AckAt(l.Value.Ticket, now) let done = try Count() if done.Done != held.Done + 1 || done.Leased != held.Leased - 1 { return false } AckAt(l.Value.Ticket, now) catch { case Unknown: return true case Failed: return false } false case None: false } }`,
	"jobs Queue.NoTicketIsUnknown":     `NoTicketIsUnknown(sec int64) { let t = Ticket{ID: -1, Attempt: 1} let now = time.Unix(sec, 0) AckAt(t, now) catch { case Failed: return false case Unknown as u: if u.Ticket != t { return false } FailAt(t, "no job", now) catch { case Unknown: return true case Failed: return false } return false } false }`,
	"jobs NoneLost":                    `NoneLost(steps []step) { let p = Policy{Retry: schedule.Recurs(2), Lease: 10ms} let mut s = Empty[int]() let mut now = time.Unix(0, 0) let mut enqueued = 0 let mut tickets = []Ticket{} for st in steps { let op Op[int] = match st { case enqueueStep as e: now = now.Add(msOf(e.Wait)) Enqueue[int]{Job: enqueued * 7, At: now} case reserveStep as r: now = now.Add(msOf(r.Wait)) Reserve{Now: now} case ackStep as a: now = now.Add(msOf(a.Wait)) let t = if len(tickets) == 0 { Ticket{ID: 0, Attempt: 0} } else { tickets[((a.Pick%len(tickets))+len(tickets))%len(tickets)] } Ack{Ticket: t, Now: now} case failStep as f: now = now.Add(msOf(f.Wait)) let t = if len(tickets) == 0 { Ticket{ID: 0, Attempt: 0} } else { tickets[((f.Pick%len(tickets))+len(tickets))%len(tickets)] } Fail{Ticket: t, Why: "failed", Now: now} } let (o, next, _) = Apply(p, s, op) s = next match o { case Enqueued as e: if e.ID != enqueued + 1 { return false } enqueued = enqueued + 1 case Reserved[int] as r: match r.Lease { case Some[Lease[int]] as l: if l.Value.Job != (l.Value.Ticket.ID - 1) * 7 { return false } for t in tickets { if t.ID == l.Value.Ticket.ID && t.Attempt >= l.Value.Ticket.Attempt { return false } } tickets = tickets.Append(l.Value.Ticket) case None: } case Settled, NotLeased: } let c = CountOf(s) if c.Waiting + c.Leased + c.Done + c.Dead != enqueued || c.Waiting < 0 || c.Leased < 0 { return false } } true }`,
	"jobs AttemptsFollowTheSchedule":   `AttemptsFollowTheSchedule(base int, retries int, limit int) { let n = ((retries%5)+5)%5 let backoff = schedule.Exponential(time.Times(1ms, 1+((base%10)+10)%10), 2.0, 50ms) let p = Policy{Retry: schedule.UpTo(schedule.And(backoff, schedule.Recurs(n)), time.Times(1ms, ((limit%80)+80)%80)), Lease: 1s} let start = time.Unix(1000, 0) let (_, s0, _) = Apply(p, Empty[string](), Enqueue[string]{Job: "job", At: start}) let mut s = s0 let mut at = start for attempt in 1..n+2 { let (o, s1, _) = Apply(p, s, Reserve{Now: at}) s = s1 let t = match o { case Reserved[string] as r: match r.Lease { case Some[Lease[string]] as l: l.Value.Ticket case None: return false } case _: return false } if t.Attempt != attempt { return false } let failedAt = at.Add(1ms) let (_, s2, _) = Apply(p, s, Fail{Ticket: t, Why: "try \(attempt)", Now: failedAt}) s = s2 match schedule.Decide(p.Retry, attempt, failedAt.Sub(start)) { case schedule.Continue as c: at = failedAt.Add(c.After) let (early, s3, _) = Apply(p, s, Reserve{Now: at.Add(-1ns)}) s = s3 if early != (Reserved[string]{Lease: None}) { return false } case schedule.Stop: let dead = DeadOf(s) return len(dead) == 1 && dead[0].Attempts == attempt && dead[0].Why == "try \(attempt)" && CountOf(s) == Counts{Waiting: 0, Leased: 0, Done: 0, Dead: 1} } } false }`,
	"jobs LeaseEndIsAFailure":          `LeaseEndIsAFailure(sec int64, ms int) { let p = Policy{Retry: schedule.Forever(), Lease: time.Times(1ms, 1+((ms%100)+100)%100)} let now = time.Unix(sec%1000000, 0) let (_, s0, _) = Apply(p, Empty[int](), Enqueue[int]{Job: 1, At: now}) let (o1, s1, _) = Apply(p, s0, Reserve{Now: now}) let first = match o1 { case Reserved[int] as r: match r.Lease { case Some[Lease[int]] as l: l.Value case None: return false } case _: return false } let (late, _, _) = Apply(p, s1, Ack{Ticket: first.Ticket, Now: first.Until}) if first.Until != now.Add(p.Lease) || late != (NotLeased{Ticket: first.Ticket}) { return false } let (o2, s2, _) = Apply(p, s1, Reserve{Now: first.Until}) let (o3, _, _) = Apply(p, s2, Ack{Ticket: first.Ticket, Now: first.Until}) match o2 { case Reserved[int] as r: match r.Lease { case Some[Lease[int]] as l: l.Value.Ticket == Ticket{ID: first.Ticket.ID, Attempt: 2} && o3 == NotLeased{Ticket: first.Ticket} case None: false } case _: false } }`,
	"jobs EarliestFirst":               `EarliestFirst(offsets []int) { let p = Policy{Retry: schedule.Forever(), Lease: 1s} let start = time.Unix(0, 0) let mut s = Empty[int]() for i, o in offsets { let (_, next, _) = Apply(p, s, Enqueue[int]{Job: i, At: start.Add(time.Times(1ms, ((o%5)+5)%5))}) s = next } let mut prevAt = start let mut prevID = 0 for _ in offsets { let (o, next, _) = Apply(p, s, Reserve{Now: start.Add(10ms)}) s = next let l = match o { case Reserved[int] as r: match r.Lease { case Some[Lease[int]] as found: found.Value case None: return false } case _: return false } let at = start.Add(time.Times(1ms, ((offsets[l.Job]%5)+5)%5)) let c = at.Compare(prevAt) if c < 0 || (c == 0 && l.Ticket.ID < prevID) || l.Ticket.ID != l.Job + 1 { return false } prevAt = at prevID = l.Ticket.ID } true }`,
	"jobs/file FileIsMemory":           `FileIsMemory(steps []script, torn string) { let p = jobs.Policy{Retry: schedule.Recurs(2), Lease: 10ms} let disk = fstest.NewMemory() let want = try memory.New[string](p) let mut got = try Open(disk, disk, "jobs.log", p, Text) let mut now = time.Unix(0, 0) let mut n = 0 let mut tickets = []jobs.Ticket{} for st in steps { match st { case enqueueAt as e: now = now.Add(time.Times(1ms, ((e.Wait%20)+20)%20)) n = n + 1 if try want.EnqueueAt("job \(n)\t\n", now) != try got.EnqueueAt("job \(n)\t\n", now) { return false } case reserveAt as r: now = now.Add(time.Times(1ms, ((r.Wait%20)+20)%20)) let a = try want.ReserveAt(now) if a != try got.ReserveAt(now) { return false } match a { case Some[jobs.Lease[string]] as l: tickets = tickets.Append(l.Value.Ticket) case None: } case ackAt as a: now = now.Add(time.Times(1ms, ((a.Wait%20)+20)%20)) let t = if len(tickets) == 0 { jobs.Ticket{ID: 0, Attempt: 0} } else { tickets[((a.Pick%len(tickets))+len(tickets))%len(tickets)] } if try acked(want, t, now) != try acked(got, t, now) { return false } case failAt as f: now = now.Add(time.Times(1ms, ((f.Wait%20)+20)%20)) let t = if len(tickets) == 0 { jobs.Ticket{ID: 0, Attempt: 0} } else { tickets[((f.Pick%len(tickets))+len(tickets))%len(tickets)] } if try failed(want, t, now) != try failed(got, t, now) { return false } case reopen: got = try Open(disk, disk, "jobs.log", p, Text) } } if try got.Count() != try want.Count() || try got.DeadLetters() != try want.DeadLetters() { return false } try disk.AppendFile("jobs.log", "enq\t" + strings.ReplaceAll(torn, "\n", "")) let back = try Open(disk, disk, "jobs.log", p, Text) if try back.Count() != try want.Count() || try back.DeadLetters() != try want.DeadLetters() { return false } let _ = try back.EnqueueAt(torn, now) let _ = try want.EnqueueAt(torn, now) let again = try Open(disk, disk, "jobs.log", p, Text) try again.Count() == try want.Count() && try again.ReserveAt(now) == try want.ReserveAt(now) }`,
	"jobs/file OpenRefusesABrokenLine": `OpenRefusesABrokenLine(job string, line string) { let p = jobs.Policy{Retry: schedule.Forever(), Lease: 1s} let disk = fstest.NewMemory() let q = try Open(disk, disk, "jobs.log", p, Text) let _ = try q.EnqueueAt(job, time.Unix(0, 0)) try disk.AppendFile("jobs.log", "x" + strings.ReplaceAll(line, "\n", "") + "\n") let _ = Open(disk, disk, "jobs.log", p, Text) catch { case jobs.Failed: return true } false }`,
}

// stdJobsLawTestName is the probe's test of a ledger key: the last
// element of the package path, capitalized, then the law without its dot
// ("ratelimit/memory AgreesWithTheModel" is MemoryAgreesWithTheModel).
func stdJobsLawTestName(key string) string {
	pkg, law, _ := strings.Cut(key, " ")
	return stdLawsTestName(pkg[strings.LastIndex(pkg, "/")+1:], law)
}

// TestStdJobsLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. It builds Go because the laws run in the built test binary (ergo test), the only place a law runs; the file queue's laws write through fstest.Memory, which runs in that binary.
func TestStdJobsLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdjobs"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdjobs failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, stdJobsLawStatements, stdJobsLawTestName, stdJobsLawsProbe, stdout))
}

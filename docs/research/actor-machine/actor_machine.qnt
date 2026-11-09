// MU3 machine-actor and MU4 machine-work: the live runner of a machine
// (std actor.StartMachine, runtime/v1 actor.go), as a Quint model. Senders
// admit signals into one inbox of capacity CAP: a Send, or an Ask that
// waits for its answer. The runner takes the front envelope and steps it
// to completion before it takes the next (run to completion). Each entry
// of the machine (the first state, an Enter, an Emit) may arm one timer: a
// child that sleeps and then pushes a timer envelope tagged with its
// entry's epoch into the same inbox. When an entry ends, the runner stops
// the timer child and waits until it has ended before the next entry arms
// its own (exit before entry); a timer envelope of an ended entry that is
// already in the inbox is dropped, never stepped (no stale signal). Done
// ends the actor: the inbox closes, queued Asks are answered Stopped, and
// nothing is stepped after it. Stop (from outside) closes admission,
// interrupts the runner (an Ask in flight is answered Stopped) and drains
// the inbox the same way. The step's decision is a free choice in every
// step, so every decision sequence is explored.
//
// MU4 adds the entry's work: a second child of the entry, started after
// the commit (the new epoch) and before observers see the new state. A
// running work pushes result envelopes ("result": a success or a typed
// failure through its map, one or more for a listener) tagged with its
// entry's epoch, or one "defect" envelope, which ends the actor whatever
// its epoch. When the entry ends, the runner interrupts the work and waits
// for its end before the next entry starts its own; an interrupted work
// pushes nothing. A result of an ended entry in the inbox is dropped.
//
// Invariants (`safe`):
//   - runToCompletion: at most one envelope is in a step;
//   - noStaleStep: no timer or result envelope of an ended entry is
//     stepped;
//   - exitBeforeEntry: no timer is armed and no work starts while the
//     previous entry's timer or work child is still alive;
//   - oneTimerPerEntry: each entry arms at most one timer (Stay keeps it);
//   - oneLiveWorkPerEntry: each entry starts at most one work;
//   - timerOnlyInItsEntry, workOnlyInItsEntry: a sleeping timer or a
//     running work that is not being stopped belongs to the current entry
//     (work starts after the commit);
//   - nothingAfterInterrupt: an interrupted work pushes no envelope;
//   - observersAfterWork: observers never see an entry whose work has not
//     started yet;
//   - noStepAfterEnd: nothing is stepped after Done, a defect or Stop;
//   - perSenderOrder: each sender's signals are stepped in the order it
//     admitted them;
//   - askAnsweredOnce: no Ask gets two answers, and when the actor has
//     ended and every sender is done, every Ask got exactly one.
//
// MUTANT selects a broken variant; each must break `safe`:
//   1 noEpoch:          the runner steps a timer envelope without its epoch check
//   2 armEarly:         the next entry arms before the old timer child ended
//   3 stayRestarts:     Stay restarts the timer, as an Enter does
//   4 noClose:          Done does not close the inbox; the runner goes on
//   5 answerTwice:      an Ask is answered twice
//   6 twoAtOnce:        the runner takes a second envelope while one is in a step
//   7 anyOrder:         the runner takes any envelope, not the front one
//   8 noWorkEpoch:      the runner steps a result envelope of an ended entry
//   9 workEarly:        the next entry's work starts before the old work ended
//  10 failOnInterrupt:  an interrupted work pushes its failure
//  11 workBeforeCommit: the work starts with the epoch before the commit
//  12 publishFirst:     observers see the new entry before its work starts
//  13 notifyTwice:      an observer reads a state and keeps it, so it gets it twice
//  14 skipBeforeStop:   an observer's loop ends at the end with states unread
//  15 staleSnapshot:    a new observer's first state is one from before its start
//  16 drainDrops:       Drain drops the last queued signal
//  17 outputLate:       an Emit's output goes to the sink after its entry
//                       is shown (the runner before fua-a)
//
// fua-a (runner rule 17): an Emit's output goes to the sink with the
// commit that gives it, so no observer is shown an Emit's state before
// its output was given (outputBeforeShown). The design gives it in the
// commit step; mutant 17 owes it until a later giveOut, after the entry
// opened and was shown.
//
// MU12 adds observers and the drain. Each observer of OBS may begin a
// Changes loop at any point (a WaitFor, a SendAndWait, a Select are such
// loops): it gets the state shown now, then each state published after
// it, from a buffer of its own, and its loop ends once the actor ended
// and it read the buffer. Every written state is published: a Stay's at
// its commit, an entry's once its children run, and one whose entry a
// stop overtook at the halt. Drain closes admission; the runner steps
// what is queued and ends when nothing is queued and no entry is open. A
// Stop may overtake a drain. Laws: changesInOrderOnce,
// waitForMatchesReplay, nothingAfterEnd, drainStepsQueued (below).
module actor_machine {
  const MUTANT: int
  const SENDERS: Set[int]

  pure val MSGS = 2
  pure val CAP = 2
  pure val IDS = SENDERS.map(i => i * MSGS).union(SENDERS.map(i => i * MSGS + 1))

  // kind is "send", "ask", "tick" (a timer), "result" or "defect" (a
  // work's); a tick, a result and a defect have sender -1.
  type Env = { kind: str, sender: int, seq: int, epoch: int }

  var inbox: List[Env]
  var nextSeq: int -> int
  var open: bool
  var stopReq: bool
  // runner is "idle", "busy" (stepping cur) or "ended".
  var runner: str
  var cur: Env
  var inFlight: int
  var epoch: int
  // timer is the child of the entry timerEpoch: "none", "sleeping", or
  // "stopping" (the runner asked it to stop and waits for its end).
  // "sleeping" means the sleep is registered with the Scheduler: arm sets
  // it in the same step that shows the entry to observers, and the runtime
  // keeps that order (actor.go arm waits for the child's first wait; fumb-a,
  // TestActorTimerArmsBeforePublish). The interleaving is the model's own;
  // before fumb-a the runtime let the sleep register after the publish.
  var timer: str
  var timerEpoch: int
  // work is the work child of the entry workEpoch: "none", "running", or
  // "stopping" (interrupted; the runner waits for its end).
  var work: str
  var workEpoch: int
  // pending: an entry opened and its timer and work are not started yet;
  // they wait for the old children to end.
  var pending: bool
  // seen is the epoch of the last entry observers were shown.
  var seen: int
  var arms: int -> int
  var starts: int -> int
  var asked: Set[int]
  var answers: int -> int
  var lastStepped: int -> int
  var bad: Set[str]
  // MU12: ob is the observers. state counts the commits (Enter, Stay,
  // Emit; state i is the i-th written state); shown is the last state
  // published (the actor's State and the snapshot a new Changes gets).
  // For each observer o of OBS: subAt is the state it was given at its
  // subscription (-1: not subscribed), bufs the states published to it
  // that it has not read, got the states it read, done that its loop
  // ended (the actor ended and it read every state). fua-a: emits are the
  // states an Emit wrote, owed those whose output the sink has not got.
  type Obs = { state: int, shown: int, subAt: int -> int, bufs: int -> List[int],
    got: int -> List[int], done: int -> bool, emits: Set[int], owed: Set[int] }
  var ob: Obs
  // MU12: dr is the drain and the end. req: Drain was called (admission
  // closed, queued signals are still stepped). admitted and stepped are
  // the ids of the signals admitted and stepped; endBy is how the actor
  // ended: "", "done", "defect", "stop" or "drain".
  var dr: { req: bool, admitted: Set[int], stepped: Set[int], endBy: str }

  pure val OBS = Set(0, 1)
  pure val none = { kind: "none", sender: -1, seq: -1, epoch: -1 }

  // publish shows state s to every subscribed observer whose loop has not
  // ended: one copy each, in commit order.
  def publish(o: Obs, s: int): Obs =
    { ...o, shown: s, bufs: OBS.mapBy(i =>
      if (o.subAt.get(i) >= 0 and not(o.done.get(i))) o.bufs.get(i).append(s) else o.bufs.get(i)) }

  // The first entry may have a timer and a work; observers then see it.
  action init = all {
    nondet t = Set("none", "sleeping").oneOf()
    nondet w = Set("none", "running").oneOf()
    all {
      timer' = t,
      work' = w,
      arms' = if (t == "sleeping") Map(0 -> 1) else Map(),
      starts' = if (w == "running") Map(0 -> 1) else Map(),
    },
    inbox' = [],
    nextSeq' = SENDERS.mapBy(_ => 0),
    open' = true,
    stopReq' = false,
    runner' = "idle",
    cur' = none,
    inFlight' = 0,
    epoch' = 0,
    timerEpoch' = 0,
    workEpoch' = 0,
    pending' = false,
    seen' = 0,
    asked' = Set(),
    answers' = IDS.mapBy(_ => 0),
    lastStepped' = SENDERS.mapBy(_ => -1),
    bad' = Set(),
    ob' = { state: 0, shown: 0, subAt: OBS.mapBy(_ => -1), bufs: OBS.mapBy(_ => []),
      got: OBS.mapBy(_ => []), done: OBS.mapBy(_ => false), emits: Set(), owed: Set() },
    dr' = { req: false, admitted: Set(), stepped: Set(), endBy: "" },
  }

  def id(e: Env): int = e.sender * MSGS + e.seq

  // Answer an Ask once (twice under mutant 5); other envelopes get none.
  def answer(a: int -> int, e: Env, n: int): int -> int =
    if (e.kind == "ask") a.setBy(id(e), x => x + n) else a

  // Every queued Ask in l is answered Stopped.
  def drainAnswers(a: int -> int, l: List[Env]): int -> int =
    l.foldl(a, (acc, e) => answer(acc, e, 1))

  // A sender admits its next signal, or is refused Stopped once admission
  // closed (a refused Ask is answered by the refusal).
  action admit(i: int, k: str): bool = all {
    nextSeq.get(i) < MSGS,
    val e = { kind: k, sender: i, seq: nextSeq.get(i), epoch: 0 }
    if (open) all {
      inbox.length() < CAP,
      inbox' = inbox.append(e),
      asked' = if (k == "ask") asked.union(Set(id(e))) else asked,
      answers' = answers,
      dr' = { ...dr, admitted: dr.admitted.union(Set(id(e))) },
    } else all {
      inbox' = inbox,
      asked' = if (k == "ask") asked.union(Set(id(e))) else asked,
      answers' = answer(answers, e, 1),
      dr' = dr,
    },
    nextSeq' = nextSeq.setBy(i, x => x + 1),
    open' = open, stopReq' = stopReq, runner' = runner, cur' = cur, inFlight' = inFlight,
    epoch' = epoch, timer' = timer, timerEpoch' = timerEpoch, work' = work, workEpoch' = workEpoch,
    pending' = pending, seen' = seen, arms' = arms, starts' = starts,
    lastStepped' = lastStepped, bad' = bad, ob' = ob,
  }

  // The timer child wakes and pushes its tick, with its entry's epoch. A
  // child that was asked to stop may still push (its stop is seen at its
  // next point): that tick is stale. The push needs room, as a Send does.
  action fire = all {
    timer == "sleeping" or timer == "stopping",
    open,
    inbox.length() < CAP,
    inbox' = inbox.append({ kind: "tick", sender: -1, seq: 0, epoch: timerEpoch }),
    timer' = "none",
    nextSeq' = nextSeq, open' = open, stopReq' = stopReq, runner' = runner, cur' = cur,
    inFlight' = inFlight, epoch' = epoch, timerEpoch' = timerEpoch, work' = work,
    workEpoch' = workEpoch, pending' = pending, seen' = seen, arms' = arms, starts' = starts,
    asked' = asked, answers' = answers, lastStepped' = lastStepped, bad' = bad, ob' = ob, dr' = dr,
  }

  // A stopped timer child ends without pushing.
  action timerEnds = all {
    timer == "stopping",
    timer' = "none",
    inbox' = inbox, nextSeq' = nextSeq, open' = open, stopReq' = stopReq, runner' = runner,
    cur' = cur, inFlight' = inFlight, epoch' = epoch, timerEpoch' = timerEpoch, work' = work,
    workEpoch' = workEpoch, pending' = pending, seen' = seen, arms' = arms, starts' = starts,
    asked' = asked, answers' = answers, lastStepped' = lastStepped, bad' = bad, ob' = ob, dr' = dr,
  }

  // A running work pushes a result with its entry's epoch: a one-shot
  // work's success or mapped failure, then it ends (last), or one signal
  // of a listener, which goes on. Or it pushes a defect and ends. An
  // interrupted work pushes nothing: its push raises at its check (mutant
  // 10: it pushes its failure). A push needs room, as a Send does.
  action workPush(k: str, last: bool): bool = all {
    work == "running" or (MUTANT == 10 and work == "stopping"),
    open,
    inbox.length() < CAP,
    inbox' = inbox.append({ kind: k, sender: -1, seq: 0, epoch: workEpoch }),
    work' = if (last or k == "defect") "none" else work,
    bad' = if (work == "stopping") bad.union(Set("afterInterrupt")) else bad,
    nextSeq' = nextSeq, open' = open, stopReq' = stopReq, runner' = runner, cur' = cur,
    inFlight' = inFlight, epoch' = epoch, timer' = timer, timerEpoch' = timerEpoch,
    workEpoch' = workEpoch, pending' = pending, seen' = seen, arms' = arms, starts' = starts,
    asked' = asked, answers' = answers, lastStepped' = lastStepped, ob' = ob, dr' = dr,
  }

  // A work ends: a running listener returns with nothing to send, or an
  // interrupted work ends without pushing.
  action workEnds = all {
    work == "running" or work == "stopping",
    work' = "none",
    inbox' = inbox, nextSeq' = nextSeq, open' = open, stopReq' = stopReq, runner' = runner,
    cur' = cur, inFlight' = inFlight, epoch' = epoch, timer' = timer, timerEpoch' = timerEpoch,
    workEpoch' = workEpoch, pending' = pending, seen' = seen, arms' = arms, starts' = starts,
    asked' = asked, answers' = answers, lastStepped' = lastStepped, bad' = bad, ob' = ob, dr' = dr,
  }

  // The runner takes the front envelope (mutant 7: any one; mutant 6: also
  // while one is in a step).
  action take = all {
    runner == "idle" or (MUTANT == 6 and runner == "busy"),
    not(pending),
    not(stopReq),
    inbox.length() > 0,
    nondet j = inbox.indices().oneOf()
    val k = if (MUTANT == 7) j else 0
    val e = inbox[k]
    val signal = e.kind == "send" or e.kind == "ask"
    all {
      cur' = e,
      inbox' = inbox.slice(0, k).concat(inbox.slice(k + 1, inbox.length())),
      bad' = if (signal and e.seq < lastStepped.get(e.sender)) bad.union(Set("order")) else bad,
      lastStepped' = if (signal) lastStepped.set(e.sender, e.seq) else lastStepped,
      dr' = if (signal) { ...dr, stepped: dr.stepped.union(Set(id(e))) } else dr,
    },
    runner' = "busy",
    inFlight' = inFlight + 1,
    nextSeq' = nextSeq, open' = open, stopReq' = stopReq, epoch' = epoch, timer' = timer,
    timerEpoch' = timerEpoch, work' = work, workEpoch' = workEpoch, pending' = pending,
    seen' = seen, arms' = arms, starts' = starts, asked' = asked, answers' = answers, ob' = ob,
  }

  // A tick or a result of an ended entry is dropped (mutant 1 steps a
  // stale tick, mutant 8 a stale result). A defect is never dropped.
  def tagged = cur.kind == "tick" or cur.kind == "result"
  def stale = (cur.kind == "tick" and cur.epoch != epoch and MUTANT != 1) or
    (cur.kind == "result" and cur.epoch != epoch and MUTANT != 8)

  action drop = all {
    runner == "busy",
    stale,
    runner' = "idle", inFlight' = inFlight - 1, cur' = none,
    inbox' = inbox, nextSeq' = nextSeq, open' = open, stopReq' = stopReq, epoch' = epoch,
    timer' = timer, timerEpoch' = timerEpoch, work' = work, workEpoch' = workEpoch,
    pending' = pending, seen' = seen, arms' = arms, starts' = starts, asked' = asked,
    answers' = answers, lastStepped' = lastStepped, bad' = bad, ob' = ob, dr' = dr,
  }

  // A defect of the work ends the actor, whatever its epoch, as Done does:
  // admission closes, queued Asks are answered, and both children are
  // stopped.
  action defectEnds = all {
    runner == "busy",
    cur.kind == "defect",
    not(stopReq),
    runner' = "ended",
    open' = false,
    inbox' = [],
    answers' = drainAnswers(answers, inbox),
    timer' = if (timer == "sleeping") "stopping" else timer,
    work' = if (work == "running") "stopping" else work,
    pending' = false,
    bad' = if (bad.contains("doneSeen")) bad.union(Set("afterEnd")) else bad,
    cur' = none,
    inFlight' = inFlight - 1,
    nextSeq' = nextSeq, stopReq' = stopReq, epoch' = epoch, timerEpoch' = timerEpoch,
    workEpoch' = workEpoch, seen' = seen, arms' = arms, starts' = starts,
    asked' = asked, lastStepped' = lastStepped, ob' = ob, dr' = { ...dr, endBy: "defect" },
  }

  // The step of cur commits a decision d. Enter and Emit end the entry:
  // its timer child is asked to stop, its work is interrupted, and the new
  // entry's children wait for their end (arm). Stay keeps the entry, its
  // timer and its work (mutant 3 restarts the timer). Done ends the actor
  // (mutant 4: the actor goes on). Mutant 12 shows observers the new entry
  // at the commit, before its work starts.
  action stepIt(d: str): bool = all {
    runner == "busy",
    not(stale),
    not(stopReq),
    cur.kind != "defect",
    val badNow = bad
      .union(if (tagged and cur.epoch != epoch) Set("stale") else Set())
      .union(if (bad.contains("doneSeen")) Set("afterEnd") else Set())
    val reenter = d == "enter" or d == "emit"
    // Mutant 3: a Stay stops the entry's timer and arms a new one for the
    // same entry.
    val restart = reenter or (d == "stay" and MUTANT == 3)
    val n = if (MUTANT == 5) 2 else 1
    all {
      if (d == "done" and MUTANT != 4) all {
        runner' = "ended",
        open' = false,
        inbox' = [],
        answers' = drainAnswers(answer(answers, cur, n), inbox),
        timer' = if (timer == "sleeping") "stopping" else timer,
        work' = if (work == "running") "stopping" else work,
        epoch' = epoch,
        pending' = false,
        seen' = seen,
        bad' = badNow,
      } else all {
        runner' = "idle",
        // Mutant 4: Done closes nothing, and the actor steps on; the next
        // step after a Done is a step after the end.
        open' = open,
        inbox' = inbox,
        answers' = answer(answers, cur, n),
        timer' = if (restart and timer == "sleeping") "stopping" else timer,
        work' = if (reenter and work == "running") "stopping" else work,
        epoch' = if (reenter) epoch + 1 else epoch,
        pending' = (restart or pending),
        seen' = if (reenter and MUTANT == 12) epoch + 1 else seen,
        bad' = if (d == "done") badNow.union(Set("doneSeen")) else badNow,
      },
      // Enter, Stay and Emit write the next state. Stay shows it at
      // once; an entry shows it once its children run (arm). An Emit's
      // output is given here, with its commit (mutant 17: owed until
      // giveOut, after the entry is shown).
      ob' = if (d == "stay") publish({ ...ob, state: ob.state + 1 }, ob.state + 1)
        else if (reenter) { ...ob, state: ob.state + 1,
          emits: if (d == "emit") ob.emits.union(Set(ob.state + 1)) else ob.emits,
          owed: if (d == "emit" and MUTANT == 17) ob.owed.union(Set(ob.state + 1)) else ob.owed }
        else ob,
      dr' = if (d == "done" and MUTANT != 4) { ...dr, endBy: "done" } else dr,
    },
    cur' = none,
    inFlight' = inFlight - 1,
    nextSeq' = nextSeq, stopReq' = stopReq, timerEpoch' = timerEpoch, workEpoch' = workEpoch,
    arms' = arms, starts' = starts, asked' = asked, lastStepped' = lastStepped,
  }

  // The new entry arms its timer and starts its work, or has none, once
  // the old children ended (mutant 2: the timer at once; mutant 9: the
  // work at once), and then observers see it. Mutant 11 starts the work
  // with the epoch before the commit.
  action arm(has: bool, hasWork: bool): bool = all {
    pending,
    runner != "ended",
    MUTANT == 2 or timer == "none",
    MUTANT == 9 or work == "none",
    bad' = bad
      .union(if (timer != "none") Set("armEarly") else Set())
      .union(if (hasWork and work != "none") Set("workEarly") else Set()),
    timer' = if (has) "sleeping" else timer,
    timerEpoch' = if (has) epoch else timerEpoch,
    arms' = if (not(has)) arms
      else if (arms.keys().contains(epoch)) arms.setBy(epoch, x => x + 1)
      else arms.put(epoch, 1),
    work' = if (hasWork) "running" else work,
    workEpoch' = if (not(hasWork)) workEpoch else if (MUTANT == 11) epoch - 1 else epoch,
    starts' = if (not(hasWork)) starts
      else if (starts.keys().contains(epoch)) starts.setBy(epoch, x => x + 1)
      else starts.put(epoch, 1),
    pending' = false,
    seen' = epoch,
    inbox' = inbox, nextSeq' = nextSeq, open' = open, stopReq' = stopReq, runner' = runner,
    cur' = cur, inFlight' = inFlight, epoch' = epoch, asked' = asked, answers' = answers,
    lastStepped' = lastStepped, dr' = dr,
    // The entry's state is shown once its children run (MU12: publish).
    ob' = if (ob.shown != ob.state) publish(ob, ob.state) else ob,
  }

  // Stop from outside: admission closes at once.
  action requestStop = all {
    not(stopReq),
    stopReq' = true,
    open' = false,
    inbox' = inbox, nextSeq' = nextSeq, runner' = runner, cur' = cur, inFlight' = inFlight,
    epoch' = epoch, timer' = timer, timerEpoch' = timerEpoch, work' = work,
    workEpoch' = workEpoch, pending' = pending, seen' = seen, arms' = arms, starts' = starts,
    asked' = asked, answers' = answers, lastStepped' = lastStepped, bad' = bad, ob' = ob, dr' = dr,
  }

  // The interrupted runner ends: an Ask in its step and every queued Ask
  // are answered Stopped; the timer child is asked to stop and the work
  // is interrupted.
  action halt = all {
    stopReq,
    runner != "ended",
    runner' = "ended",
    answers' = drainAnswers(if (runner == "busy") answer(answers, cur, 1) else answers, inbox),
    inbox' = [],
    inFlight' = 0,
    cur' = none,
    timer' = if (timer == "sleeping") "stopping" else timer,
    work' = if (work == "running") "stopping" else work,
    pending' = false,
    nextSeq' = nextSeq, open' = open, stopReq' = stopReq, epoch' = epoch, timerEpoch' = timerEpoch,
    workEpoch' = workEpoch, seen' = seen, arms' = arms, starts' = starts, asked' = asked,
    lastStepped' = lastStepped, bad' = bad,
    // A state written before the stop is shown, also when its entry's
    // children had not started (MU12).
    ob' = if (ob.shown != ob.state) publish(ob, ob.state) else ob,
    dr' = { ...dr, endBy: "stop" },
  }

  // The variables an observer or a drain request leaves alone.
  action keepRunner = all {
    inbox' = inbox, nextSeq' = nextSeq, stopReq' = stopReq, runner' = runner,
    cur' = cur, inFlight' = inFlight, epoch' = epoch, timer' = timer, timerEpoch' = timerEpoch,
    work' = work, workEpoch' = workEpoch, pending' = pending, seen' = seen, arms' = arms,
    starts' = starts, asked' = asked, answers' = answers, lastStepped' = lastStepped, bad' = bad,
  }

  // MU12: observer o begins a Changes loop (a WaitFor, a SendAndWait, a
  // Select), at any point, also after the end: its first state is the one
  // shown now (mutant 15: the one before it, a state from before the
  // wait's start).
  action subscribe(o: int): bool = all {
    ob.subAt.get(o) < 0,
    val first = if (MUTANT == 15 and ob.shown > 0) ob.shown - 1 else ob.shown
    ob' = { ...ob, subAt: ob.subAt.set(o, ob.shown), bufs: ob.bufs.set(o, [first]) },
    open' = open, dr' = dr,
    keepRunner,
  }

  // Observer o reads its next state (mutant 13: it is read and kept, so
  // the observer gets it twice).
  action observe(o: int): bool = all {
    ob.subAt.get(o) >= 0,
    not(ob.done.get(o)),
    ob.bufs.get(o).length() > 0,
    val b = ob.bufs.get(o)
    ob' = { ...ob, got: ob.got.setBy(o, g => g.append(b.head())),
      bufs: ob.bufs.set(o, if (MUTANT == 13) b else b.tail()) },
    open' = open, dr' = dr,
    keepRunner,
  }

  // Observer o's loop ends once the actor ended and it read every state
  // shown to it (mutant 14: it ends at the end, with states unread, so a
  // state written just before a stop is skipped).
  action observerEnds(o: int): bool = all {
    ob.subAt.get(o) >= 0,
    not(ob.done.get(o)),
    runner == "ended",
    MUTANT == 14 or ob.bufs.get(o).length() == 0,
    ob' = { ...ob, done: ob.done.set(o, true) },
    open' = open, dr' = dr,
    keepRunner,
  }

  // MU12: Drain closes admission; the runner goes on stepping what is
  // queued (mutant 16: the last queued signal is dropped at the request).
  action requestDrain = all {
    not(dr.req),
    not(stopReq),
    dr' = { ...dr, req: true },
    open' = false,
    inbox' = if (MUTANT == 16 and inbox.length() > 0) inbox.slice(0, inbox.length() - 1) else inbox,
    nextSeq' = nextSeq, stopReq' = stopReq, runner' = runner,
    cur' = cur, inFlight' = inFlight, epoch' = epoch, timer' = timer, timerEpoch' = timerEpoch,
    work' = work, workEpoch' = workEpoch, pending' = pending, seen' = seen, arms' = arms,
    starts' = starts, asked' = asked, answers' = answers, lastStepped' = lastStepped, bad' = bad,
    ob' = ob,
  }

  // A drained runner ends when nothing is queued and no step or entry is
  // open: the timer child is stopped and the work interrupted, as at
  // every end.
  action drainEnds = all {
    dr.req,
    not(stopReq),
    runner == "idle",
    not(pending),
    inbox.length() == 0,
    runner' = "ended",
    timer' = if (timer == "sleeping") "stopping" else timer,
    work' = if (work == "running") "stopping" else work,
    dr' = { ...dr, endBy: "drain" },
    inbox' = inbox, nextSeq' = nextSeq, open' = open, stopReq' = stopReq, cur' = cur,
    inFlight' = inFlight, epoch' = epoch, timerEpoch' = timerEpoch, workEpoch' = workEpoch,
    pending' = pending, seen' = seen, arms' = arms, starts' = starts, asked' = asked,
    answers' = answers, lastStepped' = lastStepped, bad' = bad, ob' = ob,
  }

  // Mutant 17: the sink gets the owed outputs once the entry opened (the
  // runner before fua-a called it after open's publish).
  action giveOut = all {
    ob.owed.size() > 0,
    not(pending),
    runner != "ended",
    ob' = { ...ob, owed: Set() },
    open' = open, dr' = dr,
    keepRunner,
  }

  action stutter = all {
    open' = open, ob' = ob, dr' = dr,
    keepRunner,
  }

  action step = any {
    nondet i = SENDERS.oneOf()
    nondet k = Set("send", "ask").oneOf()
    admit(i, k),
    fire,
    timerEnds,
    nondet k = Set("result", "defect").oneOf()
    nondet last = Set(true, false).oneOf()
    workPush(k, last),
    workEnds,
    take,
    drop,
    defectEnds,
    nondet d = Set("enter", "stay", "emit", "ignore", "reject", "done").oneOf()
    stepIt(d),
    nondet has = Set(true, false).oneOf()
    nondet hasWork = Set(true, false).oneOf()
    arm(has, hasWork),
    requestStop,
    halt,
    nondet o = OBS.oneOf()
    any { subscribe(o), observe(o), observerEnds(o) },
    requestDrain,
    drainEnds,
    giveOut,
    stutter,
  }

  def sendersDone = SENDERS.forall(i => nextSeq.get(i) == MSGS)

  val runToCompletion = inFlight <= 1
  val noStaleStep = not(bad.contains("stale"))
  val exitBeforeEntry = not(bad.contains("armEarly")) and not(bad.contains("workEarly"))
  val oneTimerPerEntry = arms.keys().forall(e => arms.get(e) <= 1)
  val oneLiveWorkPerEntry = starts.keys().forall(e => starts.get(e) <= 1)
  val timerOnlyInItsEntry = timer != "sleeping" or timerEpoch == epoch or pending
  val workOnlyInItsEntry = work != "running" or workEpoch == epoch or pending
  val nothingAfterInterrupt = not(bad.contains("afterInterrupt"))
  val observersAfterWork = not(pending and seen == epoch)
  val noStepAfterEnd = not(bad.contains("afterEnd"))
  val perSenderOrder = not(bad.contains("order"))
  val askAnsweredOnce = IDS.forall(x => answers.get(x) <= 1) and
    (not(runner == "ended" and sendersDone) or asked.forall(x => answers.get(x) == 1))
  val safe = runToCompletion and noStaleStep and exitBeforeEntry and oneTimerPerEntry and
    oneLiveWorkPerEntry and timerOnlyInItsEntry and workOnlyInItsEntry and
    nothingAfterInterrupt and observersAfterWork and noStepAfterEnd and perSenderOrder and
    askAnsweredOnce and changesInOrderOnce and waitForMatchesReplay and nothingAfterEnd and
    drainStepsQueued and outputBeforeShown

  // fua-a: no state an observer is shown, or may read, owes its output.
  val outputBeforeShown = ob.owed.forall(i => i > ob.shown)

  // MU12 laws over the observers and the drain.
  // ChangesInOrderOnce: each observer reads consecutive written states,
  // each once, in commit order, whatever the number of observers.
  val changesInOrderOnce = OBS.forall(o =>
    ob.got.get(o).indices().forall(i => ob.got.get(o)[i] == ob.subAt.get(o) + i))
  // WaitForMatchesReplay: an observer's first state is the one shown at
  // its start, so with changesInOrderOnce its states are a replay of the
  // written states from its start (never one from before it).
  val waitForMatchesReplay = OBS.forall(o =>
    ob.got.get(o).length() == 0 or ob.got.get(o)[0] == ob.subAt.get(o))
  // NothingAfterEnd: an observer's loop ends after the actor ended, having
  // read the last written state (a state written just before a stop
  // counts), and no state is written after it.
  val nothingAfterEnd = OBS.forall(o =>
    not(ob.done.get(o)) or
      (runner == "ended" and ob.got.get(o).length() > 0 and ob.got.get(o)[ob.got.get(o).length() - 1] == ob.state))
  // DrainStepsQueued: an actor that a drain ended stepped every signal it
  // admitted.
  val drainStepsQueued = dr.endBy != "drain" or dr.admitted.subseteq(dr.stepped)

  // Witnesses: the design must reach each of these, so `safe` is not
  // vacuous. Each `no…` value is the negation; a run must violate it.
  val noStaleTick = not(runner == "busy" and cur.kind == "tick" and cur.epoch != epoch)
  val noStaleResult = not(runner == "busy" and cur.kind == "result" and cur.epoch != epoch)
  val noInterruptedWork = work != "stopping"
  val noDefectEnd = not(runner == "busy" and cur.kind == "defect")
  val noEndAfterAsks = not(runner == "ended" and sendersDone and asked.size() >= 2)
  val noFullInbox = inbox.length() < CAP
  val noTwoSendersQueued = not(inbox.length() == CAP and inbox[0].sender != inbox[1].sender and inbox[0].sender >= 0 and inbox[1].sender >= 0)
  // MU12 witnesses: an observer reads three states to the end; a wait
  // starts after two commits; a stop ends the actor while an observer has
  // states unread; a drain ends the actor after two admitted signals.
  val noLongObserver = not(OBS.exists(o => ob.done.get(o) and ob.got.get(o).length() >= 3))
  val noLateWait = not(OBS.exists(o => ob.subAt.get(o) >= 2))
  val noStopOvertakes = not(runner == "ended" and dr.endBy == "stop" and
    OBS.exists(o => not(ob.done.get(o)) and ob.bufs.get(o).length() > 0 and ob.subAt.get(o) >= 0))
  val noDrainEnd = not(dr.endBy == "drain" and dr.admitted.size() >= 2)
  // fua-a witness: an observer reads a state an Emit wrote.
  val noEmitSeen = not(OBS.exists(o =>
    ob.got.get(o).indices().exists(i => ob.emits.contains(ob.got.get(o)[i]))))
}

module design2 { import actor_machine(MUTANT = 0, SENDERS = Set(0, 1)).* }
module design3 { import actor_machine(MUTANT = 0, SENDERS = Set(0, 1, 2)).* }
module noEpoch { import actor_machine(MUTANT = 1, SENDERS = Set(0, 1)).* }
module armEarly { import actor_machine(MUTANT = 2, SENDERS = Set(0, 1)).* }
module stayRestarts { import actor_machine(MUTANT = 3, SENDERS = Set(0, 1)).* }
module noClose { import actor_machine(MUTANT = 4, SENDERS = Set(0, 1)).* }
module answerTwice { import actor_machine(MUTANT = 5, SENDERS = Set(0, 1)).* }
module twoAtOnce { import actor_machine(MUTANT = 6, SENDERS = Set(0, 1)).* }
module anyOrder { import actor_machine(MUTANT = 7, SENDERS = Set(0, 1)).* }
module noWorkEpoch { import actor_machine(MUTANT = 8, SENDERS = Set(0, 1)).* }
module workEarly { import actor_machine(MUTANT = 9, SENDERS = Set(0, 1)).* }
module failOnInterrupt { import actor_machine(MUTANT = 10, SENDERS = Set(0, 1)).* }
module workBeforeCommit { import actor_machine(MUTANT = 11, SENDERS = Set(0, 1)).* }
module publishFirst { import actor_machine(MUTANT = 12, SENDERS = Set(0, 1)).* }
module notifyTwice { import actor_machine(MUTANT = 13, SENDERS = Set(0, 1)).* }
module skipBeforeStop { import actor_machine(MUTANT = 14, SENDERS = Set(0, 1)).* }
module staleSnapshot { import actor_machine(MUTANT = 15, SENDERS = Set(0, 1)).* }
module drainDrops { import actor_machine(MUTANT = 16, SENDERS = Set(0, 1)).* }
module outputLate { import actor_machine(MUTANT = 17, SENDERS = Set(0, 1)).* }

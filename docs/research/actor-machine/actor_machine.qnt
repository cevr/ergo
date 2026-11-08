// MU3 machine-actor: the live runner of a machine (std actor.StartMachine,
// runtime/v1 actor.go), as a Quint model. Senders admit signals into one
// inbox of capacity CAP: a Send, or an Ask that waits for its answer. The
// runner takes the front envelope and steps it to completion before it
// takes the next (run to completion). Each entry of the machine (the first
// state, an Enter, an Emit) may arm one timer: a child that sleeps and
// then pushes a timer envelope tagged with its entry's epoch into the same
// inbox. When an entry ends, the runner stops the timer child and waits
// until it has ended before the next entry arms its own (exit before
// entry); a timer envelope of an ended entry that is already in the inbox
// is dropped, never stepped (no stale signal). Done ends the actor: the
// inbox closes, queued Asks are answered Stopped, and nothing is stepped
// after it. Stop (from outside) closes admission, interrupts the runner
// (an Ask in flight is answered Stopped) and drains the inbox the same way.
// The step's decision is a free choice in every step, so every decision
// sequence is explored.
//
// Invariants (`safe`):
//   - runToCompletion: at most one envelope is in a step;
//   - noStaleStep: no timer envelope of an ended entry is stepped;
//   - exitBeforeEntry: no timer is armed while the previous entry's timer
//     child is still alive;
//   - oneTimerPerEntry: each entry arms at most one timer (Stay keeps it);
//   - timerOnlyInItsEntry: a sleeping timer that is not being stopped
//     belongs to the current entry;
//   - noStepAfterEnd: nothing is stepped after Done or Stop;
//   - perSenderOrder: each sender's signals are stepped in the order it
//     admitted them;
//   - askAnsweredOnce: no Ask gets two answers, and when the actor has
//     ended and every sender is done, every Ask got exactly one.
//
// MUTANT selects a broken variant; each must break `safe`:
//   1 noEpoch:      the runner steps a timer envelope without its epoch check
//   2 armEarly:     the next entry arms before the old timer child ended
//   3 stayRestarts: Stay restarts the timer, as an Enter does
//   4 noClose:      Done does not close the inbox; the runner goes on
//   5 answerTwice:  an Ask is answered twice
//   6 twoAtOnce:    the runner takes a second envelope while one is in a step
//   7 anyOrder:     the runner takes any envelope, not the front one
module actor_machine {
  const MUTANT: int
  const SENDERS: Set[int]

  pure val MSGS = 2
  pure val CAP = 2
  pure val IDS = SENDERS.map(i => i * MSGS).union(SENDERS.map(i => i * MSGS + 1))

  // kind is "send", "ask" or "tick" (a timer); a tick has sender -1.
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
  var timer: str
  var timerEpoch: int
  // pending: an entry opened and its timer is not armed yet; it waits for
  // the old child to end.
  var pending: bool
  var arms: int -> int
  var asked: Set[int]
  var answers: int -> int
  var lastStepped: int -> int
  var bad: Set[str]

  pure val none = { kind: "none", sender: -1, seq: -1, epoch: -1 }

  // The first entry may have a timer.
  action init = nondet t = Set("none", "sleeping").oneOf() all {
    inbox' = [],
    nextSeq' = SENDERS.mapBy(_ => 0),
    open' = true,
    stopReq' = false,
    runner' = "idle",
    cur' = none,
    inFlight' = 0,
    epoch' = 0,
    timer' = t,
    timerEpoch' = 0,
    pending' = false,
    arms' = if (t == "sleeping") Map(0 -> 1) else Map(),
    asked' = Set(),
    answers' = IDS.mapBy(_ => 0),
    lastStepped' = SENDERS.mapBy(_ => -1),
    bad' = Set(),
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
    } else all {
      inbox' = inbox,
      asked' = if (k == "ask") asked.union(Set(id(e))) else asked,
      answers' = answer(answers, e, 1),
    },
    nextSeq' = nextSeq.setBy(i, x => x + 1),
    open' = open, stopReq' = stopReq, runner' = runner, cur' = cur, inFlight' = inFlight,
    epoch' = epoch, timer' = timer, timerEpoch' = timerEpoch, pending' = pending, arms' = arms,
    lastStepped' = lastStepped, bad' = bad,
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
    inFlight' = inFlight, epoch' = epoch, timerEpoch' = timerEpoch, pending' = pending, arms' = arms,
    asked' = asked, answers' = answers, lastStepped' = lastStepped, bad' = bad,
  }

  // A stopped timer child ends without pushing.
  action timerEnds = all {
    timer == "stopping",
    timer' = "none",
    inbox' = inbox, nextSeq' = nextSeq, open' = open, stopReq' = stopReq, runner' = runner,
    cur' = cur, inFlight' = inFlight, epoch' = epoch, timerEpoch' = timerEpoch, pending' = pending,
    arms' = arms, asked' = asked, answers' = answers, lastStepped' = lastStepped, bad' = bad,
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
    all {
      cur' = e,
      inbox' = inbox.slice(0, k).concat(inbox.slice(k + 1, inbox.length())),
      bad' = if (e.kind != "tick" and e.seq < lastStepped.get(e.sender)) bad.union(Set("order")) else bad,
      lastStepped' = if (e.kind != "tick") lastStepped.set(e.sender, e.seq) else lastStepped,
    },
    runner' = "busy",
    inFlight' = inFlight + 1,
    nextSeq' = nextSeq, open' = open, stopReq' = stopReq, epoch' = epoch, timer' = timer,
    timerEpoch' = timerEpoch, pending' = pending, arms' = arms, asked' = asked, answers' = answers,
  }

  // A tick of an ended entry is dropped (mutant 1 steps it instead).
  def stale = cur.kind == "tick" and cur.epoch != epoch and MUTANT != 1

  action drop = all {
    runner == "busy",
    stale,
    runner' = "idle", inFlight' = inFlight - 1, cur' = none,
    inbox' = inbox, nextSeq' = nextSeq, open' = open, stopReq' = stopReq, epoch' = epoch,
    timer' = timer, timerEpoch' = timerEpoch, pending' = pending, arms' = arms, asked' = asked,
    answers' = answers, lastStepped' = lastStepped, bad' = bad,
  }

  // The step of cur commits a decision d. Enter and Emit end the entry:
  // its timer child is asked to stop, and the new entry's timer waits for
  // its end (arm). Stay keeps the entry and its timer (mutant 3 restarts
  // it). Done ends the actor (mutant 4: the actor goes on).
  action stepIt(d: str): bool = all {
    runner == "busy",
    not(stale),
    not(stopReq),
    val badNow = bad
      .union(if (cur.kind == "tick" and cur.epoch != epoch) Set("stale") else Set())
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
        epoch' = epoch,
        pending' = false,
        bad' = badNow,
      } else all {
        runner' = "idle",
        // Mutant 4: Done closes nothing, and the actor steps on; the next
        // step after a Done is a step after the end.
        open' = open,
        inbox' = inbox,
        answers' = answer(answers, cur, n),
        timer' = if (restart and timer == "sleeping") "stopping" else timer,
        epoch' = if (reenter) epoch + 1 else epoch,
        pending' = (restart or pending),
        bad' = if (d == "done") badNow.union(Set("doneSeen")) else badNow,
      },
    },
    cur' = none,
    inFlight' = inFlight - 1,
    nextSeq' = nextSeq, stopReq' = stopReq, timerEpoch' = timerEpoch, arms' = arms,
    asked' = asked, lastStepped' = lastStepped,
  }

  // The new entry arms its timer, or has none, once the old child ended
  // (mutant 2: at once).
  action arm(has: bool): bool = all {
    pending,
    runner != "ended",
    MUTANT == 2 or timer == "none",
    bad' = if (timer != "none") bad.union(Set("armEarly")) else bad,
    timer' = if (has) "sleeping" else timer,
    timerEpoch' = if (has) epoch else timerEpoch,
    arms' = if (not(has)) arms
      else if (arms.keys().contains(epoch)) arms.setBy(epoch, x => x + 1)
      else arms.put(epoch, 1),
    pending' = false,
    inbox' = inbox, nextSeq' = nextSeq, open' = open, stopReq' = stopReq, runner' = runner,
    cur' = cur, inFlight' = inFlight, epoch' = epoch, asked' = asked, answers' = answers,
    lastStepped' = lastStepped,
  }

  // Stop from outside: admission closes at once.
  action requestStop = all {
    not(stopReq),
    stopReq' = true,
    open' = false,
    inbox' = inbox, nextSeq' = nextSeq, runner' = runner, cur' = cur, inFlight' = inFlight,
    epoch' = epoch, timer' = timer, timerEpoch' = timerEpoch, pending' = pending, arms' = arms,
    asked' = asked, answers' = answers, lastStepped' = lastStepped, bad' = bad,
  }

  // The interrupted runner ends: an Ask in its step and every queued Ask
  // are answered Stopped; the timer child is asked to stop.
  action halt = all {
    stopReq,
    runner != "ended",
    runner' = "ended",
    answers' = drainAnswers(if (runner == "busy") answer(answers, cur, 1) else answers, inbox),
    inbox' = [],
    inFlight' = 0,
    cur' = none,
    timer' = if (timer == "sleeping") "stopping" else timer,
    pending' = false,
    nextSeq' = nextSeq, open' = open, stopReq' = stopReq, epoch' = epoch, timerEpoch' = timerEpoch,
    arms' = arms, asked' = asked, lastStepped' = lastStepped, bad' = bad,
  }

  action stutter = all {
    inbox' = inbox, nextSeq' = nextSeq, open' = open, stopReq' = stopReq, runner' = runner,
    cur' = cur, inFlight' = inFlight, epoch' = epoch, timer' = timer, timerEpoch' = timerEpoch,
    pending' = pending, arms' = arms, asked' = asked, answers' = answers,
    lastStepped' = lastStepped, bad' = bad,
  }

  action step = any {
    nondet i = SENDERS.oneOf()
    nondet k = Set("send", "ask").oneOf()
    admit(i, k),
    fire,
    timerEnds,
    take,
    drop,
    nondet d = Set("enter", "stay", "emit", "ignore", "reject", "done").oneOf()
    stepIt(d),
    nondet has = Set(true, false).oneOf()
    arm(has),
    requestStop,
    halt,
    stutter,
  }

  def sendersDone = SENDERS.forall(i => nextSeq.get(i) == MSGS)

  val runToCompletion = inFlight <= 1
  val noStaleStep = not(bad.contains("stale"))
  val exitBeforeEntry = not(bad.contains("armEarly"))
  val oneTimerPerEntry = arms.keys().forall(e => arms.get(e) <= 1)
  val timerOnlyInItsEntry = timer != "sleeping" or timerEpoch == epoch or pending
  val noStepAfterEnd = not(bad.contains("afterEnd"))
  val perSenderOrder = not(bad.contains("order"))
  val askAnsweredOnce = IDS.forall(x => answers.get(x) <= 1) and
    (not(runner == "ended" and sendersDone) or asked.forall(x => answers.get(x) == 1))
  val safe = runToCompletion and noStaleStep and exitBeforeEntry and oneTimerPerEntry and
    timerOnlyInItsEntry and noStepAfterEnd and perSenderOrder and askAnsweredOnce

  // Witnesses: the design must reach each of these, so `safe` is not
  // vacuous. Each `no…` value is the negation; a run must violate it.
  val noStaleTick = not(runner == "busy" and cur.kind == "tick" and cur.epoch != epoch)
  val noEndAfterAsks = not(runner == "ended" and sendersDone and asked.size() >= 2)
  val noFullInbox = inbox.length() < CAP
  val noTwoSendersQueued = not(inbox.length() == CAP and inbox[0].sender != inbox[1].sender and inbox[0].sender >= 0 and inbox[1].sender >= 0)
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

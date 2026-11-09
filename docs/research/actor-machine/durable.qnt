// MU9 (survey M54, em-parity row 35): a durable machine actor, std
// actor.StartDurable and journal.Start, as a Quint model. The step is pure
// (MQ5), so a state is a fold of the signals the actor committed. The
// runner takes a signal from its inbox, the step decides, and for a commit
// (Enter, Stay, Emit) the save appends the signal to the journal before
// the runner commits the state and before it answers the Ask. Every EVERY
// saves, the journal takes a snapshot of the state after its signals and
// drops them. A work's signal carries the epoch of its entry; one of an
// ended entry is dropped, never stepped, so never saved. The actor may
// crash between any two actions: the inbox and the live state are lost,
// the journal stays. Recovery starts from the snapshot and replays the
// signals after it with the pure step.
//
// Invariants (`safe`):
//   - AnsweredIsJournaled: the transition of each Ask answered with a
//     commit is in the journal (an Ignore or a Reject saves nothing; history is the journal's ghost: every signal it took, also
//     those a snapshot dropped);
//   - LiveIsTheFold: a live actor between steps is in the state the pure
//     step folds from the initial state over the journal, also after a
//     recovery.
//
// MUTANT selects a broken variant; each must break the invariant named:
//   1 saveAfterAnswer: the runner commits and answers, then saves
//                      (AnsweredIsJournaled)
//   2 saveAtSend:      a work's signal is journaled when the work sends it,
//                      not when it is stepped, so a signal of an ended
//                      entry is replayed (LiveIsTheFold)
//   3 staleSnapshot:   a snapshot keeps the first snapshot's state and
//                      drops the signals after it (LiveIsTheFold)
module durable {
  const MUTANT: int

  pure val INIT = 0
  pure val EVERY = 2
  pure val MAXIDS = 7
  pure val MAXINBOX = 3

  // Signals: 0 Ignore, 1 Stay (+1), 2 Enter (+2), 3 Reject; the work's
  // signal 4 Stays (+3). States are mod 5.
  pure def commits(e: int): bool = e == 1 or e == 2 or e == 4
  pure def nextOf(s: int, e: int): int =
    if (e == 1) (s + 1) % 5
    else if (e == 2) (s + 2) % 5
    else if (e == 4) (s + 3) % 5
    else s
  pure def replay(s: int, es: List[int]): int = es.foldl(s, (acc, e) => nextOf(acc, e))

  type Env = { sig: int, epoch: int, internal: bool, ask: bool, id: int }

  var up: bool
  var live: int
  var epoch: int
  var inbox: List[Env]
  // phase: "idle", "decided" (the step decided a commit), "saved",
  // "committed" (the state is the actor's), "answered" (mutant 1 only).
  var phase: str
  var cur: Env
  // The journal: the snapshot's state, whether there is one, and the
  // signals after it.
  var snap: int
  var hasSnap: bool
  var rest: List[int]
  // Ghosts: every signal the journal took, the ids saved and answered.
  var history: List[int]
  var saved: Set[int]
  var answered: Set[int]
  var nextId: int
  // Counts for the witnesses.
  var recoveries: int
  var snapRecoveries: int
  var dropped: int
  var crashesAfterSave: int

  action init = all {
    up' = true, live' = INIT, epoch' = 0, inbox' = List(), phase' = "idle",
    cur' = { sig: 0, epoch: 0, internal: false, ask: false, id: -1 },
    snap' = INIT, hasSnap' = false, rest' = List(), history' = List(),
    saved' = Set(), answered' = Set(), nextId' = 0,
    recoveries' = 0, snapRecoveries' = 0, dropped' = 0, crashesAfterSave' = 0,
  }

  // The journal takes e (a save, or mutant 2's send-time save), with a
  // snapshot of `after` every EVERY signals.
  action journal(e: int, id: int, after: int): bool = {
    val t = rest.append(e)
    all {
      history' = history.append(e),
      saved' = saved.union(Set(id)),
      if (t.length() >= EVERY) all {
        snap' = if (MUTANT == 3 and hasSnap) snap else after,
        hasSnap' = true,
        rest' = List(),
      } else all { snap' = snap, hasSnap' = hasSnap, rest' = t },
    }
  }

  action keepJournal = all {
    history' = history, saved' = saved, snap' = snap, hasSnap' = hasSnap, rest' = rest,
  }

  action keepCounts = all {
    recoveries' = recoveries, snapRecoveries' = snapRecoveries, dropped' = dropped,
    crashesAfterSave' = crashesAfterSave,
  }

  // send: an outside Send or Ask is admitted.
  action send(e: int, ask: bool): bool = all {
    up, inbox.length() < MAXINBOX, nextId < MAXIDS, e != 4,
    inbox' = inbox.append({ sig: e, epoch: epoch, internal: false, ask: ask, id: nextId }),
    nextId' = nextId + 1,
    up' = up, live' = live, epoch' = epoch, phase' = phase, cur' = cur,
    answered' = answered, keepJournal, keepCounts,
  }

  // workSend: the current entry's work sends signal 4, tagged with the
  // entry's epoch. Mutant 2 journals it here.
  action workSend: bool = all {
    up, inbox.length() < MAXINBOX, nextId < MAXIDS,
    inbox' = inbox.append({ sig: 4, epoch: epoch, internal: true, ask: false, id: nextId }),
    nextId' = nextId + 1,
    if (MUTANT == 2) journal(4, nextId, live) else keepJournal,
    up' = up, live' = live, epoch' = epoch, phase' = phase, cur' = cur,
    answered' = answered, keepCounts,
  }

  // take: the runner takes the oldest envelope and steps it. A work's
  // signal of an ended entry is dropped; Ignore and Reject commit nothing
  // and are answered at once.
  action take: bool = {
    val env = inbox.head()
    all {
      up, phase == "idle", inbox.length() > 0,
      inbox' = inbox.tail(),
      if (env.internal and env.epoch != epoch) all {
        phase' = "idle", cur' = cur, answered' = answered,
        dropped' = dropped + 1, recoveries' = recoveries, snapRecoveries' = snapRecoveries,
        crashesAfterSave' = crashesAfterSave,
      } else if (commits(env.sig)) all {
        phase' = "decided", cur' = env, answered' = answered, keepCounts,
      } else all {
        phase' = "idle", cur' = cur, answered' = answered, keepCounts,
      },
      up' = up, live' = live, epoch' = epoch, nextId' = nextId, keepJournal,
    }
  }

  // save: the design saves after the decision; mutant 1 after the answer.
  // Mutant 2 saved a work's signal at send already.
  action save: bool = all {
    up,
    if (MUTANT == 1) phase == "answered" else phase == "decided",
    if (MUTANT == 2 and cur.internal) keepJournal
    else journal(cur.sig, cur.id, if (MUTANT == 1) live else nextOf(live, cur.sig)),
    phase' = if (MUTANT == 1) "idle" else "saved",
    up' = up, live' = live, epoch' = epoch, inbox' = inbox, cur' = cur,
    answered' = answered, nextId' = nextId, keepCounts,
  }

  // commit: the state becomes the actor's; an Enter opens a new entry.
  action commit: bool = all {
    up,
    if (MUTANT == 1) phase == "decided" else phase == "saved",
    live' = nextOf(live, cur.sig),
    epoch' = if (cur.sig == 2) epoch + 1 else epoch,
    phase' = "committed",
    up' = up, inbox' = inbox, cur' = cur, answered' = answered, nextId' = nextId,
    keepJournal, keepCounts,
  }

  // answer: the asker gets the decision.
  action answer: bool = all {
    up, phase == "committed",
    answered' = if (cur.ask) answered.union(Set(cur.id)) else answered,
    phase' = if (MUTANT == 1) "answered" else "idle",
    up' = up, live' = live, epoch' = epoch, inbox' = inbox, cur' = cur,
    nextId' = nextId, keepJournal, keepCounts,
  }

  // crash: the process dies: the inbox, the step in progress and the live
  // state are lost; the journal stays.
  action crash: bool = all {
    up,
    up' = false, phase' = "idle", inbox' = List(),
    crashesAfterSave' = if (phase == "saved") crashesAfterSave + 1 else crashesAfterSave,
    live' = live, epoch' = epoch, cur' = cur, answered' = answered, nextId' = nextId,
    recoveries' = recoveries, snapRecoveries' = snapRecoveries, dropped' = dropped,
    keepJournal,
  }

  // recover: a new incarnation starts from the snapshot and replays the
  // signals after it; its first entry is a new epoch.
  action recover: bool = all {
    not(up),
    up' = true,
    live' = replay(if (hasSnap) snap else INIT, rest),
    epoch' = epoch + 1,
    recoveries' = recoveries + 1,
    snapRecoveries' = if (hasSnap) snapRecoveries + 1 else snapRecoveries,
    phase' = "idle", inbox' = List(), cur' = cur, answered' = answered, nextId' = nextId,
    dropped' = dropped, crashesAfterSave' = crashesAfterSave, keepJournal,
  }

  action step = {
    nondet e = Set(0, 1, 2, 3).oneOf()
    nondet ask = Set(true, false).oneOf()
    any { send(e, ask), workSend, take, save, commit, answer, crash, recover }
  }

  val AnsweredIsJournaled = answered.subseteq(saved)
  val LiveIsTheFold = (up and phase == "idle") implies live == replay(INIT, history)
  val safe = AnsweredIsJournaled and LiveIsTheFold

  // Witnesses: each `no…` must be violated by the design, so `safe` is
  // not vacuous: a recovery from a snapshot, a dropped work signal, a
  // crash after a save and before its commit, an answered Ask.
  val noSnapRecovery = snapRecoveries == 0
  val noDrop = dropped == 0
  val noCrashAfterSave = crashesAfterSave == 0
  val noAnswer = answered.size() == 0
}

module design { import durable(MUTANT = 0).* }
module saveAfterAnswer { import durable(MUTANT = 1).* }
module saveAtSend { import durable(MUTANT = 2).* }
module staleSnapshot { import durable(MUTANT = 3).* }

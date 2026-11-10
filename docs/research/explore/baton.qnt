// X5 sched-seam: the baton protocol of the explored build (runtime.md,
// Explored schedules), as a Quint model. Task 0 is the root and starts
// with the baton; tasks 1.. are started by the task that holds it. Only
// the holder acts. At a point it gives the baton back and the strategy
// picks any enabled task, itself included. A wait parks the holder, which
// gives the baton to another enabled task. The holder may wake a parked
// task (it becomes enabled; it runs only when picked), sleep (it parks on
// a timer), or end. The clock fires a sleeper only when no task is
// enabled. When no task is enabled, none sleeps, and some have not ended,
// the run is a deadlock and stops.
//
// Invariants (`safe`):
//   - OneBaton: at most one task runs, and it is the holder;
//   - NoParkedHolder: the holder is never parked or ended;
//   - ClockOnlyWhenIdle: the clock never fired while a task was enabled;
//   - DeadlockHonest: a reported deadlock had no enabled task and no
//     sleeper, and some task had not ended.
// Witnesses (each must be violated): noDeadlock, noTick, noEnd.
//
// MUTANT selects a broken variant; each must break the invariant named:
//   1 twoBatons:  a point hands the baton on and the holder keeps running
//                 (OneBaton)
//   2 earlyTick:  the clock fires a sleeper while a task is enabled
//                 (ClockOnlyWhenIdle)
//   3 lostWake:   a wake does not enable the task, and a deadlock is
//                 reported while it sleeps (DeadlockHonest)
//
// The Go form is runtime/rt/sched_explore.go; TestSchedulerFollowsModel
// is its executable model. Once the explorer's layer 1 (X1) lands, this
// model is ported to an Ergo machine and deleted (owner q78).
module baton {
  const N: int
  const MUTANT: int

  // status: "new" (not started), "ready", "run", "parked", "sleep", "done"
  var status: int -> str
  var holder: int
  var started: int
  var over: bool
  var deadlock: bool
  var tickedEnabled: bool
  var deadlockLied: bool
  var ticks: int

  pure val Tasks = 0.to(N - 1)

  def canRun(t: int): bool = status.get(t) == "ready" or status.get(t) == "run"
  def anyEnabled: bool = Tasks.exists(t => canRun(t))
  def anySleeping: bool = Tasks.exists(t => status.get(t) == "sleep")
  def allDone: bool = Tasks.forall(t => status.get(t) == "done" or status.get(t) == "new")

  action init = all {
    status' = Tasks.mapBy(t => if (t == 0) "run" else "new"),
    holder' = 0,
    started' = 1,
    over' = false,
    deadlock' = false,
    tickedEnabled' = false,
    deadlockLied' = false,
    ticks' = 0,
  }

  // handoff gives the baton to some enabled task of s, or ends the run.
  action handoff(s: int -> str): bool = {
    val cands = Tasks.filter(t => s.get(t) == "ready" or s.get(t) == "run")
    if (cands.size() > 0)
      nondet p = oneOf(cands)
      all {
        status' = Tasks.mapBy(t =>
          if (t == p) "run"
          else if (s.get(t) == "run" and MUTANT != 1) "ready"
          else s.get(t)),
        holder' = p,
        over' = false,
        deadlock' = false,
        deadlockLied' = deadlockLied, ticks' = ticks,
      }
    else if (Tasks.exists(t => s.get(t) == "sleep"))
      // Every task waits and a timer is armed: the clock fires one.
      nondet w = oneOf(Tasks.filter(t => s.get(t) == "sleep"))
      all {
        status' = s.set(w, "run"),
        holder' = w,
        over' = false,
        deadlock' = false,
        deadlockLied' = deadlockLied, ticks' = ticks + 1,
      }
    else all {
      status' = s,
      holder' = -1,
      over' = true,
      deadlock' = not(Tasks.forall(t => s.get(t) == "done" or s.get(t) == "new")),
      deadlockLied' = deadlockLied, ticks' = ticks,
    }
  }

  action point = all {
    not(over),
    handoff(status),
    started' = started, tickedEnabled' = tickedEnabled,
  }

  action spawn = all {
    not(over), started < N,
    status' = status.set(started, "ready"),
    started' = started + 1,
    holder' = holder, over' = over, deadlock' = deadlock,
    tickedEnabled' = tickedEnabled, deadlockLied' = deadlockLied, ticks' = ticks,
  }

  action park = all {
    not(over),
    handoff(status.set(holder, "parked")),
    started' = started, tickedEnabled' = tickedEnabled,
  }

  action sleep = all {
    not(over),
    handoff(status.set(holder, "sleep")),
    started' = started, tickedEnabled' = tickedEnabled,
  }

  action wake = all {
    not(over),
    nondet j = oneOf(Tasks)
    all {
      status.get(j) == "parked",
      status' = status.set(j, if (MUTANT == 3) "parked" else "ready"),
      // The lost wake leaves a task that its waker meant to run: a later
      // deadlock is a false report.
      deadlockLied' = (deadlockLied or MUTANT == 3),
    },
    started' = started, holder' = holder, over' = over, deadlock' = deadlock,
    tickedEnabled' = tickedEnabled, ticks' = ticks,
  }

  action end = all {
    not(over),
    handoff(status.set(holder, "done")),
    started' = started, tickedEnabled' = tickedEnabled,
  }

  // The clock fires a sleeper; in the design only when no task is enabled
  // (handoff does that), the mutant fires one at any time.
  action earlyTick = all {
    not(over), MUTANT == 2, anySleeping,
    nondet w = oneOf(Tasks.filter(t => status.get(t) == "sleep"))
    status' = status.set(w, "ready"),
    tickedEnabled' = (tickedEnabled or anyEnabled),
    ticks' = ticks + 1,
    started' = started, holder' = holder, over' = over, deadlock' = deadlock,
    deadlockLied' = deadlockLied,
  }

  action stutter = all {
    over,
    status' = status, holder' = holder, started' = started, over' = over,
    deadlock' = deadlock, tickedEnabled' = tickedEnabled,
    deadlockLied' = deadlockLied, ticks' = ticks,
  }

  action step = any { point, spawn, park, sleep, wake, end, earlyTick, stutter }

  val OneBaton = Tasks.forall(t => status.get(t) == "run" implies t == holder)
  val NoParkedHolder = holder == -1 or status.get(holder) == "run"
  val ClockOnlyWhenIdle = not(tickedEnabled)
  val DeadlockHonest = not(deadlock and deadlockLied)
  val safe = OneBaton and NoParkedHolder and ClockOnlyWhenIdle and DeadlockHonest

  val noDeadlock = not(deadlock)
  val noEnd = not(over and not(deadlock))
  val noTick = ticks == 0
}

module design { import baton(N = 3, MUTANT = 0).* }
module twoBatons { import baton(N = 3, MUTANT = 1).* }
module earlyTick { import baton(N = 3, MUTANT = 2).* }
module lostWake { import baton(N = 3, MUTANT = 3).* }

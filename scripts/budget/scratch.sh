# scratch.sh, sourced by light.sh and heavy.sh: each run gets its own TMPDIR and GOTMPDIR, a folder under one
# scratch root (ERGO_BUDGET_SCRATCH, default ergo-budget in the caller's TMPDIR, else /tmp: the volume a run's
# scratch was on before), removed when the run ends. A run killed before it
# can remove its folder leaves it, with its lock file; the next run's sweep removes every folder whose lock no run
# holds. The lock is fd 9 of the run, held by the run and the processes it starts, so a folder is never swept
# while anything of its run still runs. (2026-11-20: killed runs had left hundreds of /tmp/ergo-mutants-*,
# /tmp/Test* and /tmp/go-build* folders, tens of GiB.)
scratch_root=${ERGO_BUDGET_SCRATCH:-${TMPDIR:-/tmp}/ergo-budget}
# A removal is of the run's folder itself: rm -r removes a symbolic link inside it, never what it points to, and a
# folder that is itself a link is removed as the link.

# scratch_remove FOLDER: repair owner access before removing a run's tree.
scratch_remove() {
  case "$1" in "$scratch_root"/?*) ;; *) return 0 ;; esac
  case "${1#"$scratch_root"/}" in . | .. | */*) return 0 ;; esac
  case "$1" in /*) ;; *) set -- "$(pwd -P)/$1" ;; esac
  # Walk in pre-order so chmod can open a directory for the walk itself.
  # Never follow links. The absolute operand needs no -- after the mode
  # (which BSD chmod does not accept).
  find -P "$1" -xdev -type d ! -perm -0700 -exec chmod u+rwx '{}' ';' >/dev/null 2>&1 || :
  # Keep the lock file if the tree still needs a later sweep. Report only
  # the final failure; cleanup never changes the wrapper's command status.
  if ! { rm -rf "$1" && rm -f "$1.lock"; } 2>/dev/null; then
    printf 'budget scratch: could not remove %s\n' "$1" >&2
  fi
  return 0
}

# scratch_sweep: remove each run folder whose lock no run holds.
scratch_sweep() {
  mkdir -p "$scratch_root" 2>/dev/null || return 0
  for l in "$scratch_root"/*.lock; do
    [ -e "$l" ] || continue
    flock -n "$l" true 2>/dev/null && scratch_remove "${l%.lock}"
  done
  return 0
}

# scratch_make NAME: make the run's folder NAME and hold its lock on fd 9; export TMPDIR and GOTMPDIR.
scratch_make() {
  scratch_dir=$scratch_root/$1
  mkdir -p "$scratch_root" && exec 9>"$scratch_dir.lock" && flock 9 && mkdir -p "$scratch_dir" || return 1
  export TMPDIR="$scratch_dir" GOTMPDIR="$scratch_dir"
}

# scratch_drop: remove the run's folder and its lock file.
scratch_drop() {
  [ -n "${scratch_dir:-}" ] && scratch_remove "$scratch_dir"
  return 0
}

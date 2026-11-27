package main

const checkHelp = `usage: ergo check [PATH...] [--strict] [--json] [-j N] [--fresh]
       ergo check --want FIXTURE|DIR...

Check Ergo source files, packages, or modules and evaluate pure laws.
Omitting PATH checks the current package or module.

Examples:
  ergo check                 Check the current package or module
  ergo check ./cmd/app       Check one package folder
  ergo check --json          Print full diagnostic report as JSON
  ergo check --strict        Fail if any law is left undecided

Options:
  --strict           fail policy on warnings or undecided laws
  --json             emit full DiagnosticReport JSON (with lint advice)
  -j, --jobs N       parallel workers for multi-package checks
  --fresh            ignore cached answers and verdicts
  --seed N           property seed for check-time law evaluation
  --lint-config FILE lint configuration override (with --json)
  --rules MANIFEST   extra lint rule pack manifest (with --json)
  --want             verify checker diagnostic fixtures

Exit codes: 0 passed; 1 check or policy failed; 2 invalid invocation.
Run ergo check --help-all for internal evaluation budget flags.
`

const checkAllHelp = checkHelp + `
Advanced options:
  --eval-steps N     evaluation step budget for check-time laws
  --eval-bytes N     evaluation allocation budget in bytes
  --target TARGET    backend target (deprecated compat)
`

const testHelp = `usage: ergo test [PATH...] [OPTIONS]

Run tests and laws using the fastest sound execution path automatically.
Multi-package runs execute in parallel by default (-j 1 serializes).
Omitting PATH tests the current package or module.

Examples:
  ergo test                    Test the current package or module
  ergo test -k Parse           Run tests or laws matching "Parse"
  ergo test --skip Slow        Skip tests or laws matching "Slow"
  ergo test --fresh -j 4       Bypass caches and run with 4 workers
  ergo test ./a ./b/...        Test explicit targets and subtrees

Options:
  -k, --filter PATTERN  run only tests or laws matching regex/name PATTERN
  --skip PATTERN        skip tests or laws matching regex/name PATTERN
  --fresh               ignore cached answers and stored verdicts
  -j, --jobs N          worker count (-j 1 to serialize)
  --seed N              random seed for property tests and laws
  --states N            search state budget for machine laws
  --receipt FILE|DIR    write law receipt JSON
  --affected [BASE]     test only packages/obligations affected since BASE
  --list                list affected targets without running (with --affected)
  --why                 explain why each target is affected (with --affected)
  --prune-cases         prune stored passing cases from ergo.cases
  --locked              require current proved entries in ergo.laws
  --debug               keep symbol table and debug info
  --json                emit JSON output (for check failures)

Use ergo verify {mutants,explore,conform,cases} for heavy verification modes.
Exit codes: 0 all passed; 1 test or law failed; 2 defect or invalid invocation.
Run ergo test --help-all for sharding, timings, and compiler debugging flags.
`

const testAllHelp = testHelp + `
Sharding, tracing & internal options:
  --trace                    print scene test steps after running one package
  --parallel                 enable parallel runner explicitly (default for multi-pkg)
  --shard i/n                run 1-indexed shard i of n
  --timings FILE             timing history JSON for shard balancing
  --update-timings[=FILE]    merge measured durations into timings JSON
  --emitted                  print emitted module digest without building
  -work, --work              keep and print emitted module directory on stderr
  --go-trace                 link runtime/obs tracer support
  --fallback on|off          control text fallback on IR lowering gaps
  --law NAME                 run only law NAME (use -k/--filter instead)
  --skip-law NAME            skip law NAME (use --skip instead)
  --run NAME                 run one test or law NAME (use -k/--filter instead)
  --count 1                  disable test result cache reads/writes (use --fresh)
  --mutants / --explore / --cases   prefer ergo verify {mutants,explore,cases}
`

const buildHelp = `usage: ergo build [PATH] [-o PATH] [--locked] [--debug] [--release] [--platform OS/ARCH]
       ergo build --lib -o DIR [PATH]

Build an executable binary or Go-callable library module from Ergo source.
Omitting PATH builds the current package.

Examples:
  ergo build                       Build current package executable
  ergo build ./cmd/app -o dist/app Write binary to dist/app
  ergo build --release ./cmd/srv   Build with default.pgo profile
  ergo build --lib -o out ./mylib  Emit a Go library module in out/

Options:
  -o PATH              output file path (or output directory with --lib)
  --lib                build a Go-callable library module instead of an executable
  --locked             refuse build if ergo.laws rewrite entries are stale/missing
  --debug              keep symbol table and DWARF debug information
  --release            enable PGO optimization using default.pgo
  --platform OS/ARCH[/LEVEL]  target platform (e.g. linux/arm64, linux/amd64/v3)

Exit codes: 0 built; 1 check or build failed; 2 invalid invocation.
Run ergo build --help-all for compiler emission and debugging flags.
`

const buildAllHelp = buildHelp + `
Internal compiler & debugging options:
  --emitted              print emitted module digest without building
  -work, --work          print emitted WORK=DIR and LINK=report on stderr
  --go-text DIR          emit readable Go module sources into DIR
  --go-trace             link runtime/obs tracer support
  --fallback on|off      allow or refuse text lowering fallback
  --views=off            disable zero-copy string view optimization
  --rewrites=MODE        control law rewrites (on|proved|off|canary[=N])
  --report copies        print ownership and copy sites on stderr
`

const runAllHelp = runHelp + `
Internal debugging options:
  --fallback on|off      allow or refuse text lowering fallback
`

const fixAllHelp = fixHelp + `
Retired & migration options:
  --dry-run              preview changes without writing (use --check instead)
  --lint                 apply safe lint fixes explicitly (now default)
  --comments             strip non-doc comments forbidden by no-comments
  --std DIR              also inspect/rewrite standard library folder DIR
  --go                   also rewrite Ergo source embedded in .go string literals
  --len                  rewrite built-in len(e) calls to e.Len()
  --global-funcs         rewrite legacy global functions to std package calls
  --std-types            rewrite legacy prelude types to std package imports
  --machines             rewrite legacy machine types to machines.* imports
  --option               rewrite legacy option types to option.* imports
`

const verifyHelp = `usage: ergo verify SUBCOMMAND [OPTIONS]

Run heavy verification workflows: mutation testing, schedule exploration,
trace conformance checking, and CLI golden cases.

Examples:
  ergo verify mutants                    Verify hand mutant ledgers under .
  ergo verify mutants --derived --strict Derive typed mutants and fail on survivors
  ergo verify explore dfs ./pkg          Explore concurrent schedules with DFS
  ergo verify conform --machine M --trace t.jsonl
  ergo verify cases ./cmd/app --cases ./cases

Subcommands:
  mutants    verify hand and derived AST mutants against tests and laws
  explore    search or replay concurrent task interleavings
  conform    verify an execution trace conforms to a machine specification
  cases      run black-box golden input/output cases against a built program

Run ergo verify SUBCOMMAND --help for subcommand flags and exit codes.
`

const verifyMutantsHelp = `usage: ergo verify mutants [PATH...] [OPTIONS]

Verify hand mutant ledgers (testdata/mutants.json) and/or derived typed AST
mutants in memory without modifying files on disk. Omitting PATH scans .

Examples:
  ergo verify mutants
  ergo verify mutants --derived --strict ./pkg
  ergo verify mutants -k "^Add" --fresh

Options:
  --derived              generate typed mutants from checked .ergo ASTs
  --strict               fail if any derived mutant in a law-covered decl survives
  --fresh                bypass cached mutant verdicts
  -k, --filter PATTERN   run only mutants whose name matches regex PATTERN
  --skip PATTERN         skip mutants whose name matches regex PATTERN
  --seed N               seed for derived/property checks
  --states N             state budget for machine laws
  --json                 emit full JSON report

Exit codes: 0 all killed; 1 survivor, invalid, or unjudged mutant; 2 usage error.
`

const verifyExploreHelp = `usage: ergo verify explore [MODE] [PATH] [OPTIONS]

Explore concurrent schedules or replay a failing schedule token.
MODE may be dfs, pct, or random (or omit MODE for cooperative run).

Examples:
  ergo verify explore dfs ./pkg
  ergo verify explore pct ./pkg --depth 3 --schedules 200
  ergo verify explore ./pkg --schedule 'TOKEN'

Options:
  -k, --filter PATTERN   run matching test or law
  --preempt B            max preemptions for dfs (default 2)
  --depth D              bug depth for pct (default 3)
  --schedules S          schedule budget
  --schedule TOKEN       replay one recorded schedule token
  --seed N               random seed
  --fresh                ignore cached answers
  --debug                keep debug info in explored binary

Exit codes: 0 passed; 1 schedule failed; 2 defect or invalid invocation.
`

const verifyConformHelp = `usage: ergo verify conform [DIR] --machine M --trace FILE [OPTIONS]

Check that a JSON Lines execution trace is a valid behaviour of machine M in DIR
(defaults to .).

Examples:
  ergo verify conform --machine Shop --trace run.jsonl
  ergo verify conform ./shop --machine Shop --trace run.jsonl --hidden 1 --json

Options:
  --machine M      machine declaration name (required)
  --trace FILE     JSON Lines trace file (required)
  --hidden N       up to N unobserved steps before each observation (0)
  --stutter        allow lines with no signal to keep state unchanged
  --limit N        maximum candidate states (100000)
  --eval-steps N   evaluator step budget
  --json           emit structured JSON verdict

Exit codes: 0 conforms; 20 mismatch; 21 trace error; 22 limit reached; 23 law broken; 1 runtime/check error; 2 usage error.
`

const verifyCasesHelp = `usage: ergo verify cases [FILE|DIR] --cases CASES_DIR [--locked]

Build the program in FILE|DIR (defaults to .) and run recorded golden I/O case
folders in CASES_DIR against the built binary.

Examples:
  ergo verify cases ./cmd/app --cases ./cases
  ergo verify cases --cases ./testdata/cases --locked

Options:
  --cases DIR      directory holding golden case subdirectories (required)
  --locked         require current proved entries in ergo.laws

Exit codes: 0 all cases pass; 1 case failure or layout error; 2 usage error.
`

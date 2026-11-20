package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// std sql (stdlib.md, Host packages, sql; wf10 TX1): sql.Transaction runs
// its block in a transaction on the DB of its context, and sqltest.Script
// keeps every call it is given. The laws are stated over a Script's log.

// sqlLawsProbe restates each law whose ledger runner is TestSQLLaws, as a
// test of the same name (TestStdLawsRun's naming). halt and stop are the
// laws' own failure of a block; script is sqltest.NewScript.
const sqlLawsProbe = `import queue
import slices
import sql
import sqltest
import task
import time

type halt {}

func stop() fails halt { halt }

func script(rules []sqltest.Rule) sqltest.Script { sqltest.NewScript(rules) }

test SqltestCommitsOnce(a string, b string, n int) {
	let s = script([]sqltest.Rule{})
	let got = with context { sql.DB: s } {
		sql.Transaction() {
			let _ = try sql.Tx.Exec(a, []sql.Value{})
			let _ = try sql.DB.Exec(b, []sql.Value{sql.Int{N: int64(n)}})
			n
		} catch {
		case _:
			n + 1
		}
	}
	assert got == n
	assert slices.Equal(s.Log(), []string{"BEGIN", a, b, "COMMIT"})
}

test SqltestRollsBackOnFailure(a string, constraint string) {
	let s = script([]sqltest.Rule{})
	let first = with context { sql.DB: s } {
		sql.Transaction() {
			let _ = try sql.Tx.Exec(a, []sql.Value{})
			try stop()
			false
		} catch {
		case halt:
			true
		case _:
			false
		}
	}
	let refused = script([]sqltest.Rule{sqltest.Refuse{Statement: a, With: sql.Unique{Constraint: constraint}}})
	let second = with context { sql.DB: refused } {
		sql.Transaction() {
			let _ = try sql.Tx.Exec(a, []sql.Value{})
			true
		} catch {
		case sql.Unique as u:
			u.Constraint == constraint
		case _:
			false
		}
	}
	let want = []string{"BEGIN", a, "ROLLBACK"}
	assert first
	assert second
	assert slices.Equal(s.Log(), want)
	assert slices.Equal(refused.Log(), want)
}

test SqltestCommitFailureEndsIt(a string, message string) {
	let s = script([]sqltest.Rule{sqltest.Refuse{Statement: "COMMIT", With: sql.Serialization{Message: message}}})
	let failed = with context { sql.DB: s } {
		sql.Transaction() {
			let _ = try sql.Tx.Exec(a, []sql.Value{})
			false
		} catch {
		case sql.Serialization as x:
			x.Message == message
		case _:
			false
		}
	}
	assert failed
	assert slices.Equal(s.Log(), []string{"BEGIN", a, "COMMIT"})
}

test SqltestRollsBackOnInterrupt(a string) {
	let s = script([]sqltest.Rule{})
	let started = queue.New[bool](1)
	let t = with context { sql.DB: s, Scheduler: time.NewManual(0) } {
		task.Go() {
			sql.Transaction() {
				let _ = try sql.Tx.Exec(a, []sql.Value{})
				try started.Push(true)
				Scheduler.Sleep(1h)
			} catch {
			case _:
			}
		}
	}
	let _ = try started.Pop()
	t.Stop()
	assert slices.Equal(s.Log(), []string{"BEGIN", a, "ROLLBACK"})
}

test SqltestNestedFailureKeepsTheOuter(a string, b string, c string) {
	let s = script([]sqltest.Rule{})
	let ok = with context { sql.DB: s } {
		sql.Transaction() {
			let _ = try sql.Tx.Exec(a, []sql.Value{})
			sql.Transaction() {
				let _ = try sql.Tx.Exec(b, []sql.Value{})
				try stop()
			} catch {
			case _:
			}
			let _ = try sql.DB.Exec(c, []sql.Value{})
			true
		} catch {
		case _:
			false
		}
	}
	assert ok
	assert slices.Equal(s.Log(), []string{"BEGIN", a, "SAVEPOINT ergo_savepoint", b, "ROLLBACK TO SAVEPOINT ergo_savepoint", "RELEASE SAVEPOINT ergo_savepoint", c, "COMMIT"})
}

test SqltestNestedCommitReleases(a string) {
	let s = script([]sqltest.Rule{})
	let ok = with context { sql.DB: s } {
		sql.Transaction() {
			try sql.Transaction() {
				try sql.Transaction() {
					let _ = try sql.Tx.Exec(a, []sql.Value{})
				}
			}
			true
		} catch {
		case _:
			false
		}
	}
	assert ok
	assert slices.Equal(s.Log(), []string{"BEGIN", "SAVEPOINT ergo_savepoint", "SAVEPOINT ergo_savepoint_ergo_savepoint", a, "RELEASE SAVEPOINT ergo_savepoint_ergo_savepoint", "RELEASE SAVEPOINT ergo_savepoint", "COMMIT"})
}

func first(r sql.Row) int64 fails sql.Mismatch { try sql.IntAt(r, 0) }

func noRows(stmt string) bool needs sql.DB {
	let _ = sql.One(stmt, []sql.Value{}, first) catch {
	case sql.NoRows as x:
		return x.Statement == stmt
	case _:
		return false
	}
	false
}

func textRefused(r sql.Row, i int) bool {
	let _ = sql.TextAt(r, i) catch {
	case sql.Mismatch as m:
		return m.Column == i && m.Want == "text"
	}
	false
}

func intRefused(r sql.Row, i int) bool {
	let _ = sql.IntAt(r, i) catch {
	case sql.Mismatch as m:
		return m.Column == i && m.Want == "int"
	}
	false
}

test SqltestRowsReadEachRow(a string, n int64, s string) {
	let rows = []sql.Row{sql.Row{Columns: []string{"id", "name"}, Values: []sql.Value{sql.Int{N: n}, sql.Text{S: s}}}, sql.Row{Columns: []string{"id", "name"}, Values: []sql.Value{sql.Int{N: n + 1}, sql.Null}}}
	let s2 = script([]sqltest.Rule{sqltest.Answer{Statement: a, Rows: rows, Affected: 0}})
	with context { sql.DB: s2 } {
		let all = try sql.Rows(a, []sql.Value{}, first)
		let one = try sql.One(a, []sql.Value{}, first)
		let text = try sql.TextAt(rows[0], 1)
		let null = try sql.IsNull(rows[1], 1)
		assert slices.Equal(all, []int64{n, n + 1})
		assert one == n
		assert noRows(a + " ")
		assert text == s
		assert null
		assert textRefused(rows[0], 0)
		assert intRefused(rows[0], 2)
	}
}

test SqlStandardReadsSQLState(constraint string, message string) {
	let d = sql.Standard
	assert d.Classify(sql.HostError{Code: "23505", Constraint: constraint, Message: message}) == sql.Unique{Constraint: constraint}
	assert d.Classify(sql.HostError{Code: "40001", Constraint: "", Message: message}) == sql.Serialization{Message: message}
	assert d.Classify(sql.HostError{Code: "40P01", Constraint: "", Message: message}) == sql.Deadlock{Message: message}
	assert d.Classify(sql.HostError{Code: "57014", Constraint: "", Message: message}) == sql.Timeout{Message: message}
}

test SqlStandardKeepsTheRest(code string, message string) {
	if code == "23505" || code == "40001" || code == "40P01" || code == "57014" {
		return
	}
	assert sql.Standard.Classify(sql.HostError{Code: code, Constraint: "", Message: message}) == sql.Other{Code: code, Message: message}
}
`

// sqlLawStatements is the statement of each law the probe restates, the
// law's source text as the ledger writes it, by ledger key.
var sqlLawStatements = map[string]string{
	"sql StandardReadsSQLState":          `StandardReadsSQLState(constraint string, message string) { let d = Standard d.Classify(HostError{Code: "23505", Constraint: constraint, Message: message}) == Unique{Constraint: constraint} && d.Classify(HostError{Code: "40001", Constraint: "", Message: message}) == Serialization{Message: message} && d.Classify(HostError{Code: "40P01", Constraint: "", Message: message}) == Deadlock{Message: message} && d.Classify(HostError{Code: "57014", Constraint: "", Message: message}) == Timeout{Message: message} }`,
	"sql StandardKeepsTheRest":           `StandardKeepsTheRest(code string, message string) { if code == "23505" || code == "40001" || code == "40P01" || code == "57014" { return true } Standard.Classify(HostError{Code: code, Constraint: "", Message: message}) == Other{Code: code, Message: message} }`,
	"sqltest CommitsOnce":                `CommitsOnce(a string, b string, n int) { let s = NewScript([]Rule{}) let got = with context { sql.DB: s } { sql.Transaction() { let _ = try sql.Tx.Exec(a, []sql.Value{}) let _ = try sql.DB.Exec(b, []sql.Value{sql.Int{N: int64(n)}}) n } catch { case _: return false } } got == n && slices.Equal(s.Log(), []string{"BEGIN", a, b, "COMMIT"}) }`,
	"sqltest RollsBackOnFailure":         `RollsBackOnFailure(a string, constraint string) { let s = NewScript([]Rule{}) let first = with context { sql.DB: s } { sql.Transaction() { let _ = try sql.Tx.Exec(a, []sql.Value{}) try stop() false } catch { case halt: true case _: false } } let refused = NewScript([]Rule{Refuse{Statement: a, With: sql.Unique{Constraint: constraint}}}) let second = with context { sql.DB: refused } { sql.Transaction() { let _ = try sql.Tx.Exec(a, []sql.Value{}) true } catch { case sql.Unique as u: u.Constraint == constraint case _: false } } let want = []string{"BEGIN", a, "ROLLBACK"} first && second && slices.Equal(s.Log(), want) && slices.Equal(refused.Log(), want) }`,
	"sqltest CommitFailureEndsIt":        `CommitFailureEndsIt(a string, message string) { let s = NewScript([]Rule{Refuse{Statement: "COMMIT", With: sql.Serialization{Message: message}}}) let failed = with context { sql.DB: s } { sql.Transaction() { let _ = try sql.Tx.Exec(a, []sql.Value{}) false } catch { case sql.Serialization as x: x.Message == message case _: false } } failed && slices.Equal(s.Log(), []string{"BEGIN", a, "COMMIT"}) }`,
	"sqltest RollsBackOnInterrupt":       `RollsBackOnInterrupt(a string) { let s = NewScript([]Rule{}) let started = queue.New[bool](1) let t = with context { sql.DB: s, Scheduler: time.NewManual(0) } { task.Go() { sql.Transaction() { let _ = try sql.Tx.Exec(a, []sql.Value{}) try started.Push(true) Scheduler.Sleep(1h) } catch { case _: } } } let _ = started.Pop() catch { case queue.Closed: return false } t.Stop() slices.Equal(s.Log(), []string{"BEGIN", a, "ROLLBACK"}) }`,
	"sqltest NestedFailureKeepsTheOuter": `NestedFailureKeepsTheOuter(a string, b string, c string) { let s = NewScript([]Rule{}) let ok = with context { sql.DB: s } { sql.Transaction() { let _ = try sql.Tx.Exec(a, []sql.Value{}) sql.Transaction() { let _ = try sql.Tx.Exec(b, []sql.Value{}) try stop() } catch { case _: } let _ = try sql.DB.Exec(c, []sql.Value{}) true } catch { case _: false } } ok && slices.Equal(s.Log(), []string{"BEGIN", a, "SAVEPOINT ergo_savepoint", b, "ROLLBACK TO SAVEPOINT ergo_savepoint", "RELEASE SAVEPOINT ergo_savepoint", c, "COMMIT"}) }`,
	"sqltest NestedCommitReleases":       `NestedCommitReleases(a string) { let s = NewScript([]Rule{}) let ok = with context { sql.DB: s } { sql.Transaction() { try sql.Transaction() { try sql.Transaction() { let _ = try sql.Tx.Exec(a, []sql.Value{}) } } true } catch { case _: false } } ok && slices.Equal(s.Log(), []string{"BEGIN", "SAVEPOINT ergo_savepoint", "SAVEPOINT ergo_savepoint_ergo_savepoint", a, "RELEASE SAVEPOINT ergo_savepoint_ergo_savepoint", "RELEASE SAVEPOINT ergo_savepoint", "COMMIT"}) }`,
	"sqltest RowsReadEachRow":            `RowsReadEachRow(a string, n int64, s string) { let rows = []sql.Row{sql.Row{Columns: []string{"id", "name"}, Values: []sql.Value{sql.Int{N: n}, sql.Text{S: s}}}, sql.Row{Columns: []string{"id", "name"}, Values: []sql.Value{sql.Int{N: n + 1}, sql.Null}}} let script = NewScript([]Rule{Answer{Statement: a, Rows: rows, Affected: 0}}) with context { sql.DB: script } { let all = sql.Rows(a, []sql.Value{}, first) catch { case _: return false } let one = sql.One(a, []sql.Value{}, first) catch { case _: return false } let text = sql.TextAt(rows[0], 1) catch { case sql.Mismatch: return false } let null = sql.IsNull(rows[1], 1) catch { case sql.Mismatch: return false } slices.Equal(all, []int64{n, n + 1}) && one == n && noRows(a+" ") && text == s && null && textRefused(rows[0], 0) && intRefused(rows[0], 2) } }`,
}

// TestSQLLaws runs the probe with --seed 1; lawledger.Run holds that it
// restates exactly the ledger's laws whose runner is this test, each with
// the row's statement, and that each passed.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestSQLLaws(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "sqllaws", sqlLawsProbe, sqlLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

// TestSQLTxOnlyInsideTransaction is the checker fact of the plan's
// StatementOutsideNeedsNoTx: a function whose row needs sql.Tx is refused
// where no Transaction binds it (S102), and checks inside one, while a
// statement through sql.DB needs no Tx. Mutant: tx1-transaction-binds-no-tx
// (the block's row loses Tx, so the call inside is refused).
func TestSQLTxOnlyInsideTransaction(t *testing.T) {
	const move = "import sql\nimport sqltest\n\nfunc move(a string) fails sql.Unique | sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other needs sql.Tx {\n\tlet _ = try sql.Tx.Exec(a, []sql.Value{})\n}\n\n"
	outside := move + "func main() {\n\twith context { sql.DB: sqltest.NewScript([]sqltest.Rule{}) } {\n\t\tmove(\"x\") catch {\n\t\tcase _:\n\t\t}\n\t}\n}\n"
	inside := move + "func main() {\n\twith context { sql.DB: sqltest.NewScript([]sqltest.Rule{}) } {\n\t\tsql.Transaction() {\n\t\t\ttry move(\"x\")\n\t\t\tlet _ = try sql.DB.Exec(\"y\", []sql.Value{})\n\t\t} catch {\n\t\tcase _:\n\t\t}\n\t}\n}\n"
	_, ds := checkProgram(t, []pkgSource{{"/ergo-test/sqltx/main.ergo", outside}})
	if len(ds) != 1 || ds[0].Code != "S102" || !strings.Contains(ds[0].Message, "requires sql.Tx") {
		t.Errorf("a call of a function that needs sql.Tx outside a Transaction: want one S102 naming sql.Tx, got %v", ds)
	}
	if _, ds := checkProgram(t, []pkgSource{{"/ergo-test/sqltx/main.ergo", inside}}); len(ds) != 0 {
		t.Errorf("the same call inside sql.Transaction: want no diagnostic, got %v", ds)
	}
}

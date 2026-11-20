// Package sql is the body of std sql's host provider (docs/spec/stdlib.md,
// sql; wf10 TX1): a pool over Go's database/sql, and a transaction held on
// one of its connections. Std sql's Ergo code turns its values into these
// plain records and reads its failures through a Dialect; this package
// only moves them to and from database/sql.
//
// A transaction starts with BeginTx under the Go context of the task that
// began it (rt.GoContext), so a stop of that task cancels the transaction:
// database/sql then rolls it back itself, and std sql's exit action finds
// it done. Rollback ignores the stop, so the exit action of a stopped task
// still ends the transaction.
package sql

import (
	"context"
	dbsql "database/sql"
	"errors"
	"reflect"
	"sync"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The kinds of an SQLArg, one per variant of sql.Value.
const (
	KindNull = iota
	KindInt
	KindFloat
	KindBool
	KindText
	KindBytes
	KindStamp
)

// SQLArg is one argument of a statement or one column of a row, as std
// sql writes a sql.Value for the host: Kind names the variant, and the
// field of that variant holds its value (a Stamp's time as Sec and Nsec of
// Unix time, in UTC).
type SQLArg struct {
	Kind int
	N    int64
	F    float64
	B    bool
	S    string
	Data []byte
	Sec  int64
	Nsec int64
}

// SQLResult is what a statement gave: the columns of its rows and their
// cells, row after row (len(Cells) is a multiple of len(Columns)), and the
// count of rows it changed (0 for a query).
type SQLResult struct {
	Columns  []string
	Cells    []SQLArg
	Affected int64
}

// SQLHostError is a failure of the database or its driver, as std sql's
// Dialect reads it: the SQLSTATE code when the driver reports one, the
// constraint it names, and its text. Closed is a handle, a connection or a
// transaction that has ended (database/sql's ErrConnDone and ErrTxDone, or
// a pool after Close).
type SQLHostError struct {
	Code       string
	Constraint string
	Message    string
	Closed     bool
}

func (e SQLHostError) Error() string { return "sql.HostError{" + e.Code + "}: " + e.Message }

// SQLPool is a database/sql pool: std sql's Host, and the transactions
// begun on it that have not ended, by id.
type SQLPool struct {
	db   *dbsql.DB
	mu   sync.Mutex
	next int64
	txs  map[int64]*dbsql.Tx
}

// hostPool marks the runtime's type as the one std sql's pool.
func (*SQLPool) hostPool() {}

// hostError is err as a SQLHostError. A driver reports SQLSTATE through a
// method SQLState() string (pgx's PgError, lib/pq's Error) and the
// constraint through a field ConstraintName (pgx) or Constraint (lib/pq);
// any other error is its text with no code.
func hostError(err error) SQLHostError {
	if errors.Is(err, dbsql.ErrConnDone) || errors.Is(err, dbsql.ErrTxDone) || err.Error() == "sql: database is closed" {
		return SQLHostError{Message: err.Error(), Closed: true}
	}
	out := SQLHostError{Message: err.Error()}
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		out.Code = coded.SQLState()
		v := reflect.ValueOf(coded)
		for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
			if v.IsNil() {
				return out
			}
			v = v.Elem()
		}
		if v.Kind() == reflect.Struct {
			for _, name := range []string{"ConstraintName", "Constraint"} {
				if f := v.FieldByName(name); f.IsValid() && f.Kind() == reflect.String {
					out.Constraint = f.String()
					break
				}
			}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		out.Code = "57014"
	}
	return out
}

// goArgs is args as database/sql's arguments.
func goArgs(args []SQLArg) []any {
	out := make([]any, len(args))
	for i, a := range args {
		switch a.Kind {
		case KindInt:
			out[i] = a.N
		case KindFloat:
			out[i] = a.F
		case KindBool:
			out[i] = a.B
		case KindText:
			out[i] = a.S
		case KindBytes:
			out[i] = a.Data
		case KindStamp:
			out[i] = time.Unix(a.Sec, a.Nsec).UTC()
		default:
			out[i] = nil
		}
	}
	return out
}

// cell is a value database/sql scanned into an any, as an SQLArg.
func cell(v any) SQLArg {
	switch x := v.(type) {
	case nil:
		return SQLArg{Kind: KindNull}
	case int64:
		return SQLArg{Kind: KindInt, N: x}
	case float64:
		return SQLArg{Kind: KindFloat, F: x}
	case bool:
		return SQLArg{Kind: KindBool, B: x}
	case string:
		return SQLArg{Kind: KindText, S: x}
	case []byte:
		return SQLArg{Kind: KindBytes, Data: append([]byte(nil), x...)}
	case time.Time:
		return SQLArg{Kind: KindStamp, Sec: x.Unix(), Nsec: int64(x.Nanosecond())}
	default:
		return SQLArg{Kind: KindText, S: reflect.ValueOf(v).String()}
	}
}

// querier is a pool or a transaction.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (dbsql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*dbsql.Rows, error)
}

func exec(t *rt.Task, q querier, stmt string, args []SQLArg) (SQLResult, error) {
	r, err := q.ExecContext(rt.GoContext(t), stmt, goArgs(args)...)
	if err != nil {
		return SQLResult{}, hostError(err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		n = 0
	}
	return SQLResult{Affected: n}, nil
}

func query(t *rt.Task, q querier, stmt string, args []SQLArg) (SQLResult, error) {
	rows, err := q.QueryContext(rt.GoContext(t), stmt, goArgs(args)...)
	if err != nil {
		return SQLResult{}, hostError(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return SQLResult{}, hostError(err)
	}
	out := SQLResult{Columns: cols}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return SQLResult{}, hostError(err)
		}
		for _, v := range vals {
			out.Cells = append(out.Cells, cell(v))
		}
	}
	if err := rows.Err(); err != nil {
		return SQLResult{}, hostError(err)
	}
	return out, nil
}

// SQLOpen is the body of sql.Open: the pool of the database/sql driver
// named driver at dsn, checked with one ping under the caller's task. A
// driver the program does not link fails with the text database/sql gives.
//
//ergo:host
//ergo:wait caller
func SQLOpen(t *rt.Task, driver string, dsn string) (*SQLPool, error) {
	db, err := dbsql.Open(driver, dsn)
	if err != nil {
		return nil, hostError(err)
	}
	if err := db.PingContext(rt.GoContext(t)); err != nil {
		_ = db.Close()
		return nil, hostError(err)
	}
	return &SQLPool{db: db}, nil
}

// SQLExec is the body of the Host's Exec: stmt on any connection of p.
//
//ergo:host
//ergo:wait caller
func SQLExec(t *rt.Task, p *SQLPool, stmt string, args []SQLArg) (SQLResult, error) {
	return exec(t, p.db, stmt, args)
}

// SQLQuery is the body of the Host's Query.
//
//ergo:host
//ergo:wait caller
func SQLQuery(t *rt.Task, p *SQLPool, stmt string, args []SQLArg) (SQLResult, error) {
	return query(t, p.db, stmt, args)
}

// SQLBegin is the body of the Host's Begin: a transaction on one
// connection of p, under t's Go context, so a stop of t rolls it back. It
// is the transaction's id in p, which the other transaction bodies take.
//
//ergo:host
//ergo:wait caller
func SQLBegin(t *rt.Task, p *SQLPool) (int64, error) {
	tx, err := p.db.BeginTx(rt.GoContext(t), nil)
	if err != nil {
		return 0, hostError(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	if p.txs == nil {
		p.txs = map[int64]*dbsql.Tx{}
	}
	p.txs[p.next] = tx
	return p.next, nil
}

// tx is the transaction id of p, and whether to forget it (it ends).
func (p *SQLPool) tx(id int64, end bool) (*dbsql.Tx, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tx, ok := p.txs[id]
	if !ok {
		return nil, SQLHostError{Message: dbsql.ErrTxDone.Error(), Closed: true}
	}
	if end {
		delete(p.txs, id)
	}
	return tx, nil
}

// SQLTxExec is the body of a transaction's Exec.
//
//ergo:host
//ergo:wait caller
func SQLTxExec(t *rt.Task, p *SQLPool, id int64, stmt string, args []SQLArg) (SQLResult, error) {
	tx, err := p.tx(id, false)
	if err != nil {
		return SQLResult{}, err
	}
	return exec(t, tx, stmt, args)
}

// SQLTxQuery is the body of a transaction's Query.
//
//ergo:host
//ergo:wait caller
func SQLTxQuery(t *rt.Task, p *SQLPool, id int64, stmt string, args []SQLArg) (SQLResult, error) {
	tx, err := p.tx(id, false)
	if err != nil {
		return SQLResult{}, err
	}
	return query(t, tx, stmt, args)
}

// SQLCommit is the body of a transaction's Commit. A Commit that fails
// ends the transaction too (database/sql: the Tx is done either way).
//
//ergo:host
//ergo:wait caller
func SQLCommit(t *rt.Task, p *SQLPool, id int64) error {
	tx, err := p.tx(id, true)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return hostError(err)
	}
	return nil
}

// SQLRollback is the body of a transaction's Rollback. It never fails: a
// transaction that has ended already (committed, a stop of its task
// rolled it back, or its connection dropped) is ignored.
//
//ergo:host
//ergo:wait caller
func SQLRollback(t *rt.Task, p *SQLPool, id int64) {
	if tx, err := p.tx(id, true); err == nil {
		_ = tx.Rollback()
	}
}

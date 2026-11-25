package driver

import (
	"context"
	dbsql "database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sqlite "modernc.org/sqlite"
)

const (
	KindNull = iota
	KindInt
	KindFloat
	KindBool
	KindText
	KindBytes
	KindStamp
)

type Arg struct {
	Kind int
	N    int64
	F    float64
	B    bool
	S    string
	Data []byte
	Sec  int64
	Nsec int64
}

type Result struct {
	Columns  []string
	Cells    []Arg
	Affected int64
}

type Failure struct {
	Code    int
	Message string
	Closed  bool
	Timeout bool
}

func (f Failure) Error() string {
	if f.Code != 0 {
		return fmt.Sprintf("sqlite(%d): %s", f.Code, f.Message)
	}
	return f.Message
}

type DB struct {
	db   *dbsql.DB
	mu   sync.Mutex
	next int64
	txs  map[int64]*dbsql.Tx
}

func Open(ctx context.Context, dsn string, singleConn bool) (*DB, error) {
	db, err := dbsql.Open("sqlite", dsn)
	if err != nil {
		return nil, wrapError(err)
	}
	if singleConn {
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, wrapError(err)
	}
	return &DB{db: db, txs: map[int64]*dbsql.Tx{}}, nil
}

func (d *DB) Shutdown() error {
	if err := d.db.Close(); err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *DB) Exec(ctx context.Context, stmt string, args []Arg) (Result, error) {
	return execWith(ctx, d.db, stmt, args)
}

func (d *DB) Query(ctx context.Context, stmt string, args []Arg) (Result, error) {
	return queryWith(ctx, d.db, stmt, args)
}

func (d *DB) BeginImmediate(ctx context.Context) (int64, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, wrapError(err)
	}
	return d.registerTx(tx), nil
}

func (d *DB) BeginDeferred(ctx context.Context) (int64, error) {
	tx, err := d.db.BeginTx(ctx, &dbsql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, wrapError(err)
	}
	return d.registerTx(tx), nil
}

func (d *DB) registerTx(tx *dbsql.Tx) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	if d.txs == nil {
		d.txs = map[int64]*dbsql.Tx{}
	}
	d.txs[d.next] = tx
	return d.next
}

func (d *DB) lookupTx(id int64, end bool) (*dbsql.Tx, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, ok := d.txs[id]
	if !ok {
		return nil, Failure{Message: dbsql.ErrTxDone.Error(), Closed: true}
	}
	if end {
		delete(d.txs, id)
	}
	return tx, nil
}

func (d *DB) TxExec(ctx context.Context, id int64, stmt string, args []Arg) (Result, error) {
	tx, err := d.lookupTx(id, false)
	if err != nil {
		return Result{}, err
	}
	return execWith(ctx, tx, stmt, args)
}

func (d *DB) TxQuery(ctx context.Context, id int64, stmt string, args []Arg) (Result, error) {
	tx, err := d.lookupTx(id, false)
	if err != nil {
		return Result{}, err
	}
	return queryWith(ctx, tx, stmt, args)
}

func (d *DB) TxCommit(id int64) error {
	tx, err := d.lookupTx(id, true)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *DB) TxRollback(id int64) {
	if tx, err := d.lookupTx(id, true); err == nil {
		_ = tx.Rollback()
	}
}

func (d *DB) TxSavepoint(ctx context.Context, id int64, name string) error {
	if !validSavepointName(name) {
		return Failure{Message: fmt.Sprintf("invalid savepoint name %q", name)}
	}
	tx, err := d.lookupTx(id, false)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *DB) TxRelease(ctx context.Context, id int64, name string) error {
	if !validSavepointName(name) {
		return Failure{Message: fmt.Sprintf("invalid savepoint name %q", name)}
	}
	tx, err := d.lookupTx(id, false)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+name); err != nil {
		return wrapError(err)
	}
	return nil
}

func (d *DB) TxRollbackTo(ctx context.Context, id int64, name string) error {
	if !validSavepointName(name) {
		return Failure{Message: fmt.Sprintf("invalid savepoint name %q", name)}
	}
	tx, err := d.lookupTx(id, false)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+name); err != nil {
		return wrapError(err)
	}
	return nil
}

func validSavepointName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if !isLetter && !isDigit && c != '_' {
			return false
		}
	}
	return true
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (dbsql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*dbsql.Rows, error)
}

func execWith(ctx context.Context, q querier, stmt string, args []Arg) (Result, error) {
	r, err := q.ExecContext(ctx, stmt, goArgs(args)...)
	if err != nil {
		return Result{}, wrapError(err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		n = 0
	}
	return Result{Affected: n}, nil
}

func queryWith(ctx context.Context, q querier, stmt string, args []Arg) (Result, error) {
	rows, err := q.QueryContext(ctx, stmt, goArgs(args)...)
	if err != nil {
		return Result{}, wrapError(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return Result{}, wrapError(err)
	}
	out := Result{Columns: cols}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return Result{}, wrapError(err)
		}
		for _, v := range vals {
			out.Cells = append(out.Cells, cellOf(v))
		}
	}
	if err := rows.Err(); err != nil {
		return Result{}, wrapError(err)
	}
	return out, nil
}

func goArgs(args []Arg) []any {
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

func cellOf(v any) Arg {
	switch x := v.(type) {
	case nil:
		return Arg{Kind: KindNull}
	case int64:
		return Arg{Kind: KindInt, N: x}
	case float64:
		return Arg{Kind: KindFloat, F: x}
	case bool:
		return Arg{Kind: KindBool, B: x}
	case string:
		return Arg{Kind: KindText, S: x}
	case []byte:
		return Arg{Kind: KindBytes, Data: append([]byte(nil), x...)}
	case time.Time:
		return Arg{Kind: KindStamp, Sec: x.Unix(), Nsec: int64(x.Nanosecond())}
	default:
		return Arg{Kind: KindText, S: fmt.Sprint(v)}
	}
}

func wrapError(err error) Failure {
	if errors.Is(err, dbsql.ErrConnDone) || errors.Is(err, dbsql.ErrTxDone) || strings.Contains(err.Error(), "sql: database is closed") {
		return Failure{Message: err.Error(), Closed: true}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return Failure{Code: 9, Message: err.Error(), Timeout: true}
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		code := se.Code()
		return Failure{
			Code:    code,
			Message: err.Error(),
			Timeout: code == 9 || (code&0xff) == 9,
		}
	}
	return Failure{Message: err.Error()}
}

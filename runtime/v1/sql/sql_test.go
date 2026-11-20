package sql

import (
	"context"
	dbsql "database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// fake is a database/sql driver that runs nothing: it keeps each call it
// is given, one line each (as sqltest.Script logs them), answers a query
// with rows, and refuses a statement that starts with "FAIL" with an error
// that reports SQLSTATE and a constraint as pgx's PgError does.
type fake struct {
	mu       sync.Mutex
	log      []string
	rollback chan struct{}
	rows     [][]driver.Value
}

func (f *fake) keep(line string) {
	f.mu.Lock()
	f.log = append(f.log, line)
	f.mu.Unlock()
}

func (f *fake) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

// pgError has PgError's method and field.
type pgError struct {
	Code           string
	ConstraintName string
}

func (e *pgError) Error() string    { return "ERROR: duplicate key (SQLSTATE " + e.Code + ")" }
func (e *pgError) SQLState() string { return e.Code }

var fakes sync.Map // dsn → *fake

type fakeDriver struct{}

func (fakeDriver) Open(dsn string) (driver.Conn, error) {
	f, _ := fakes.Load(dsn)
	return &fakeConn{f: f.(*fake)}, nil
}

func init() { dbsql.Register("ergo-fake", fakeDriver{}) }

type fakeConn struct{ f *fake }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake: no Prepare") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.f.keep("BEGIN")
	return fakeTx{c.f}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.f.keep(q)
	if strings.HasPrefix(q, "FAIL") {
		return nil, &pgError{Code: "23505", ConstraintName: "account_pkey"}
	}
	return driver.RowsAffected(int64(len(args))), nil
}

func (c *fakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.f.keep(q)
	rows := c.f.rows
	if len(args) > 0 {
		// The query echoes its arguments as one row.
		row := make([]driver.Value, len(args))
		for i, a := range args {
			row[i] = a.Value
		}
		rows = [][]driver.Value{row}
	}
	return &fakeRows{rows: rows}, nil
}

type fakeTx struct{ f *fake }

func (t fakeTx) Commit() error { t.f.keep("COMMIT"); return nil }
func (t fakeTx) Rollback() error {
	t.f.keep("ROLLBACK")
	if t.f.rollback != nil {
		close(t.f.rollback)
	}
	return nil
}

type fakeRows struct {
	rows [][]driver.Value
	at   int
}

func (r *fakeRows) Columns() []string {
	if len(r.rows) == 0 {
		return []string{"a"}
	}
	out := make([]string, len(r.rows[0]))
	for i := range out {
		out[i] = string(rune('a' + i))
	}
	return out
}
func (r *fakeRows) Close() error { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.at == len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.at])
	r.at++
	return nil
}

func open(t *testing.T, f *fake) *SQLPool {
	t.Helper()
	fakes.Store(t.Name(), f)
	p, err := SQLOpen(rt.Root("test"), "ergo-fake", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.db.Close() })
	return p
}

// TestHostSendsTheTransaction holds the host's transaction bodies against
// the line log of sqltest.Script's laws: a transaction is BEGIN, its
// statements on its connection and one COMMIT; a stop of the task that
// began it rolls it back once, and the exit action's Rollback after it
// sends nothing more. Mutants: sql-begin-ignores-the-stop (BeginTx under
// context.Background, so a stop leaves the transaction open) and
// sql-rollback-panics-when-done (Rollback fails on a transaction that has
// ended).
func TestHostSendsTheTransaction(t *testing.T) {
	f := &fake{}
	p := open(t, f)
	task := rt.Root("tx")
	c, err := SQLBegin(task, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SQLTxExec(task, p, c, "UPDATE a", nil); err != nil {
		t.Fatal(err)
	}
	if err := SQLCommit(task, p, c); err != nil {
		t.Fatal(err)
	}
	SQLRollback(task, p, c)
	if got, want := f.lines(), []string{"BEGIN", "UPDATE a", "COMMIT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a committed transaction: log %q, want %q", got, want)
	}

	g := &fake{rollback: make(chan struct{})}
	q := open(t, g)
	stopped := rt.Root("stopped")
	c, err = SQLBegin(stopped, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SQLTxExec(stopped, q, c, "UPDATE b", nil); err != nil {
		t.Fatal(err)
	}
	stopped.Interrupt()
	<-g.rollback // database/sql rolls back when the task's Go context ends
	SQLRollback(stopped, q, c)
	if got, want := g.lines(), []string{"BEGIN", "UPDATE b", "ROLLBACK"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a stopped transaction: log %q, want %q", got, want)
	}
}

// TestHostReadsValuesAndErrors holds the host's conversions: each kind of
// argument reaches the driver as database/sql's value and comes back as
// the same kind, and a driver error with SQLSTATE and a constraint keeps
// both. Mutants: sql-stamp-loses-nanos (a Stamp sent as seconds only),
// sql-error-drops-constraint (the constraint field is not read) and
// sql-closed-not-read (a closed pool's error is not Closed).
func TestHostReadsValuesAndErrors(t *testing.T) {
	f := &fake{}
	p := open(t, f)
	task := rt.Root("values")
	at := time.Unix(1700000000, 123456789).UTC()
	args := []SQLArg{
		{Kind: KindNull},
		{Kind: KindInt, N: -7},
		{Kind: KindFloat, F: 2.5},
		{Kind: KindBool, B: true},
		{Kind: KindText, S: "ü"},
		{Kind: KindBytes, Data: []byte{0, 1}},
		{Kind: KindStamp, Sec: at.Unix(), Nsec: int64(at.Nanosecond())},
	}
	r, err := SQLQuery(task, p, "SELECT", args)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Cells, args) || len(r.Columns) != len(args) {
		t.Errorf("echoed arguments: got %+v, want %+v", r, args)
	}
	_, err = SQLExec(task, p, "FAIL insert", nil)
	var h SQLHostError
	if !errors.As(err, &h) || h.Code != "23505" || h.Constraint != "account_pkey" || h.Closed {
		t.Errorf("a unique violation: got %#v", err)
	}
	_ = p.db.Close()
	_, err = SQLExec(task, p, "UPDATE", nil)
	if !errors.As(err, &h) || !h.Closed {
		t.Errorf("a statement on a closed pool: got %#v, want Closed", err)
	}
}

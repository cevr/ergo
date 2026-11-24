package driver

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func fileDSN(path string) string {
	return fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
}

func memDSN() string {
	return ":memory:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
}

func TestPragmasAndWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pragmas.db")
	db, err := Open(ctx, fileDSN(path), false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Shutdown()

	checkText := func(pragma, want string) {
		t.Helper()
		res, err := db.Query(ctx, "PRAGMA "+pragma, nil)
		if err != nil {
			t.Fatalf("PRAGMA %s: %v", pragma, err)
		}
		if len(res.Cells) != 1 || res.Cells[0].Kind != KindText || res.Cells[0].S != want {
			t.Fatalf("PRAGMA %s = %+v, want text %q", pragma, res.Cells, want)
		}
	}
	checkInt := func(pragma string, want int64) {
		t.Helper()
		res, err := db.Query(ctx, "PRAGMA "+pragma, nil)
		if err != nil {
			t.Fatalf("PRAGMA %s: %v", pragma, err)
		}
		if len(res.Cells) != 1 || res.Cells[0].Kind != KindInt || res.Cells[0].N != want {
			t.Fatalf("PRAGMA %s = %+v, want int %d", pragma, res.Cells, want)
		}
	}

	checkText("journal_mode", "wal")
	checkInt("busy_timeout", 5000)
	checkInt("foreign_keys", 1)
	checkInt("synchronous", 1)
}

func TestBeginImmediateAcquiresLockUpFront(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "immediate.db")
	db1, err := Open(ctx, fileDSN(path), false)
	if err != nil {
		t.Fatalf("Open db1: %v", err)
	}
	defer db1.Shutdown()

	if _, err := db1.Exec(ctx, "CREATE TABLE items (id INTEGER PRIMARY KEY)", nil); err != nil {
		t.Fatalf("create table: %v", err)
	}

	shortDSN := fmt.Sprintf("file:%s?_pragma=busy_timeout(80)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
	db2, err := Open(ctx, shortDSN, false)
	if err != nil {
		t.Fatalf("Open db2: %v", err)
	}
	defer db2.Shutdown()

	tx1, err := db1.BeginImmediate(ctx)
	if err != nil {
		t.Fatalf("BeginImmediate tx1: %v", err)
	}
	defer db1.TxRollback(tx1)

	_, err = db2.BeginImmediate(ctx)
	if err == nil {
		t.Fatal("expected db2.BeginImmediate to fail with SQLITE_BUSY while tx1 holds write lock")
	}
	var f Failure
	if !errors.As(err, &f) || (f.Code&0xff) != 5 {
		t.Fatalf("expected SQLITE_BUSY (5), got %+v (%v)", f, err)
	}
}

func TestTransactionsSavepointsAndCancelRollback(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, memDSN(), true)
	if err != nil {
		t.Fatalf("Open memory: %v", err)
	}
	defer db.Shutdown()

	if _, err := db.Exec(ctx, "CREATE TABLE log (id INTEGER PRIMARY KEY, label TEXT UNIQUE NOT NULL)", nil); err != nil {
		t.Fatalf("create table: %v", err)
	}

	tx, err := db.BeginImmediate(ctx)
	if err != nil {
		t.Fatalf("BeginImmediate: %v", err)
	}
	if _, err := db.TxExec(ctx, tx, "INSERT INTO log(id, label) VALUES (?, ?)", []Arg{{Kind: KindInt, N: 1}, {Kind: KindText, S: "outer"}}); err != nil {
		t.Fatalf("insert outer: %v", err)
	}
	if err := db.TxSavepoint(ctx, tx, "sp1"); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	if _, err := db.TxExec(ctx, tx, "INSERT INTO log(id, label) VALUES (?, ?)", []Arg{{Kind: KindInt, N: 2}, {Kind: KindText, S: "inner"}}); err != nil {
		t.Fatalf("insert inner: %v", err)
	}
	if err := db.TxRollbackTo(ctx, tx, "sp1"); err != nil {
		t.Fatalf("rollback to sp1: %v", err)
	}
	if err := db.TxRelease(ctx, tx, "sp1"); err != nil {
		t.Fatalf("release sp1: %v", err)
	}
	if err := db.TxCommit(tx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	res, err := db.Query(ctx, "SELECT id, label FROM log ORDER BY id", nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Cells) != 2 || res.Cells[0].N != 1 || res.Cells[1].S != "outer" {
		t.Fatalf("unexpected rows after savepoint rollback: %+v", res.Cells)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	tx2, err := db.BeginImmediate(cancelCtx)
	if err != nil {
		t.Fatalf("BeginImmediate cancelCtx: %v", err)
	}
	if _, err := db.TxExec(cancelCtx, tx2, "INSERT INTO log(id, label) VALUES (?, ?)", []Arg{{Kind: KindInt, N: 3}, {Kind: KindText, S: "cancelled"}}); err != nil {
		t.Fatalf("insert before cancel: %v", err)
	}
	cancel()
	db.TxRollback(tx2)

	res, err = db.Query(ctx, "SELECT COUNT(*) FROM log", nil)
	if err != nil {
		t.Fatalf("count after cancel: %v", err)
	}
	if len(res.Cells) != 1 || res.Cells[0].N != 1 {
		t.Fatalf("expected 1 row after cancelled tx rollback, got %+v", res.Cells)
	}
}

func TestErrorClassificationForeignKeysAndValues(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, memDSN(), true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	schema := []string{
		"CREATE TABLE parents (id INTEGER PRIMARY KEY, code TEXT UNIQUE NOT NULL)",
		"CREATE TABLE children (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parents(id))",
		"CREATE TABLE vals (i INTEGER, f REAL, s TEXT, b BLOB, n TEXT, ts DATETIME)",
	}
	for _, stmt := range schema {
		if _, err := db.Exec(ctx, stmt, nil); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if _, err := db.Exec(ctx, "INSERT INTO parents(id, code) VALUES (?, ?)", []Arg{{Kind: KindInt, N: 1}, {Kind: KindText, S: "p1"}}); err != nil {
		t.Fatalf("insert parent: %v", err)
	}

	_, err = db.Exec(ctx, "INSERT INTO parents(id, code) VALUES (?, ?)", []Arg{{Kind: KindInt, N: 1}, {Kind: KindText, S: "p2"}})
	var f Failure
	if !errors.As(err, &f) || f.Code != 1555 {
		t.Fatalf("duplicate PK: want code 1555, got %+v (%v)", f, err)
	}

	_, err = db.Exec(ctx, "INSERT INTO parents(id, code) VALUES (?, ?)", []Arg{{Kind: KindInt, N: 2}, {Kind: KindText, S: "p1"}})
	f = Failure{}
	if !errors.As(err, &f) || f.Code != 2067 {
		t.Fatalf("duplicate UNIQUE: want code 2067, got %+v (%v)", f, err)
	}

	_, err = db.Exec(ctx, "INSERT INTO children(id, parent_id) VALUES (?, ?)", []Arg{{Kind: KindInt, N: 10}, {Kind: KindInt, N: 999}})
	f = Failure{}
	if !errors.As(err, &f) || f.Code != 787 {
		t.Fatalf("FK violation: want code 787, got %+v (%v)", f, err)
	}

	stamp := time.Date(2026, 11, 24, 12, 30, 45, 0, time.UTC)
	if _, err := db.Exec(ctx, "INSERT INTO vals(i, f, s, b, n, ts) VALUES (?, ?, ?, ?, ?, ?)", []Arg{
		{Kind: KindInt, N: 42},
		{Kind: KindFloat, F: 3.5},
		{Kind: KindText, S: "hello"},
		{Kind: KindBytes, Data: []byte{1, 2, 3}},
		{Kind: KindNull},
		{Kind: KindStamp, Sec: stamp.Unix(), Nsec: 0},
	}); err != nil {
		t.Fatalf("insert vals: %v", err)
	}
	res, err := db.Query(ctx, "SELECT i, f, s, b, n, ts FROM vals", nil)
	if err != nil {
		t.Fatalf("select vals: %v", err)
	}
	if len(res.Cells) != 6 || res.Cells[0].N != 42 || res.Cells[1].F != 3.5 || res.Cells[2].S != "hello" || len(res.Cells[3].Data) != 3 || res.Cells[4].Kind != KindNull {
		t.Fatalf("unexpected cells: %+v", res.Cells)
	}

	if err := db.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	_, err = db.Query(ctx, "SELECT 1", nil)
	f = Failure{}
	if !errors.As(err, &f) || !f.Closed {
		t.Fatalf("query after Shutdown: want Closed=true, got %+v (%v)", f, err)
	}
}

func TestConcurrentMultiWriterWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wal-concurrent.db")

	dbA, err := Open(ctx, fileDSN(path), false)
	if err != nil {
		t.Fatalf("Open dbA: %v", err)
	}
	defer dbA.Shutdown()

	if _, err := dbA.Exec(ctx, "CREATE TABLE counter (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)", nil); err != nil {
		t.Fatalf("create counter: %v", err)
	}
	if _, err := dbA.Exec(ctx, "CREATE TABLE events (seq INTEGER PRIMARY KEY AUTOINCREMENT, writer INTEGER NOT NULL, step INTEGER NOT NULL)", nil); err != nil {
		t.Fatalf("create events: %v", err)
	}
	if _, err := dbA.Exec(ctx, "INSERT INTO counter(id, n) VALUES (1, 0)", nil); err != nil {
		t.Fatalf("seed counter: %v", err)
	}

	dbB, err := Open(ctx, fileDSN(path), false)
	if err != nil {
		t.Fatalf("Open dbB: %v", err)
	}
	defer dbB.Shutdown()

	const writers = 8
	const perWriter = 25
	var wg sync.WaitGroup
	errCh := make(chan error, writers)

	for w := range writers {
		db := dbA
		if w%2 == 1 {
			db = dbB
		}
		wg.Add(1)
		go func(writerID int, pool *DB) {
			defer wg.Done()
			for step := range perWriter {
				tx, err := pool.BeginImmediate(ctx)
				if err != nil {
					errCh <- fmt.Errorf("writer %d step %d begin: %w", writerID, step, err)
					return
				}
				res, err := pool.TxQuery(ctx, tx, "SELECT n FROM counter WHERE id = 1", nil)
				if err != nil {
					pool.TxRollback(tx)
					errCh <- fmt.Errorf("writer %d step %d select: %w", writerID, step, err)
					return
				}
				next := res.Cells[0].N + 1
				if _, err := pool.TxExec(ctx, tx, "UPDATE counter SET n = ? WHERE id = 1", []Arg{{Kind: KindInt, N: next}}); err != nil {
					pool.TxRollback(tx)
					errCh <- fmt.Errorf("writer %d step %d update: %w", writerID, step, err)
					return
				}
				if _, err := pool.TxExec(ctx, tx, "INSERT INTO events(writer, step) VALUES (?, ?)", []Arg{{Kind: KindInt, N: int64(writerID)}, {Kind: KindInt, N: int64(step)}}); err != nil {
					pool.TxRollback(tx)
					errCh <- fmt.Errorf("writer %d step %d insert: %w", writerID, step, err)
					return
				}
				if err := pool.TxCommit(tx); err != nil {
					errCh <- fmt.Errorf("writer %d step %d commit: %w", writerID, step, err)
					return
				}
			}
		}(w, db)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	res, err := dbA.Query(ctx, "SELECT n FROM counter WHERE id = 1", nil)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	want := int64(writers * perWriter)
	if len(res.Cells) != 1 || res.Cells[0].N != want {
		t.Fatalf("counter = %+v, want %d", res.Cells, want)
	}
	ev, err := dbB.Query(ctx, "SELECT COUNT(*) FROM events", nil)
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	if len(ev.Cells) != 1 || ev.Cells[0].N != want {
		t.Fatalf("events count = %+v, want %d", ev.Cells, want)
	}
}

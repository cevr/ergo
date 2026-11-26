package bindc

import "sync/atomic"

// lazy is a table built on its first use: a program that links bindc and
// never reads a header pays no allocation for it at start, and a reader
// pays one atomic load per use, as cheap as a package variable.
// The zero-capture make keeps a lazy variable statically initialized.
type lazy[T any] struct {
	v    atomic.Pointer[T]
	make func() T
}

// get is the table, built by the first call.
func (l *lazy[T]) get() T {
	if p := l.v.Load(); p != nil {
		return *p
	}
	return l.build()
}

// build makes the table once; a caller that loses the race to store its
// table reads the winner's, so every caller sees the same table.
func (l *lazy[T]) build() T {
	v := l.make()
	l.v.CompareAndSwap(nil, &v)
	return *l.v.Load()
}

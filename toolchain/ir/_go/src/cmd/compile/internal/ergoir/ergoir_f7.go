// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ergoir

import (
	"strconv"
	"strings"

	"cmd/compile/internal/types2"
)

// arrayType resolves the spelling [N]T of an array type (FLIP F7): ok is
// false for any other spelling, and the type is nil when T is no type.
func (c *ergoChecker) arrayType(p ergoPos, s string) (types2.Type, bool) {
	rest, ok := strings.CutPrefix(s, "[")
	if !ok {
		return nil, false
	}
	i := strings.IndexByte(rest, ']')
	if i <= 0 {
		return nil, false
	}
	n, err := strconv.ParseInt(rest[:i], 10, 64)
	if err != nil || n < 0 {
		return nil, false
	}
	e := c.typ(p, rest[i+1:])
	if e == nil {
		return nil, true
	}
	return types2.NewArray(e, n), true
}

// ergoIndexElem is the element type of an "index" of a value of type t
// (FLIP F7): a slice's or an array's element, or a byte of a string; nil
// for any other type.
func ergoIndexElem(t types2.Type) types2.Type {
	switch u := types2.CoreType(t).(type) {
	case *types2.Slice:
		return u.Elem()
	case *types2.Array:
		return u.Elem()
	}
	if ergoIsString(t) {
		return types2.Typ[types2.Byte]
	}
	return nil
}

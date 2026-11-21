// Package gofmt gives the runtime fmt's text of a value (binsize BS2).
// The core runtime writes a value without fmt (rt.Text), which knows the
// values an Ergo program raises: texts, errors, Stringers, booleans and
// numbers, and writes a value of another Go type (from import go) as Go's
// runtime writes a panic of it. A machine state's text is its Go fields,
// which only fmt writes; a program that writes one imports this package,
// whose init makes fmt the runtime's text of every value.
package gofmt

import (
	"fmt"
	"reflect"

	"ergo.local/prototype/runtime/rt"
)

func init() { rt.UseHostText(text) }

// text is fmt's %v of v, or with typed v's Go type name and its %+v
// (Idle{n:1}).
func text(v any, typed bool) string {
	if !typed {
		return fmt.Sprint(v)
	}
	name := ""
	if t := reflect.TypeOf(v); t != nil {
		name = t.Name()
	}
	return name + fmt.Sprintf("%+v", v)
}

package syntax

import "reflect"

// IsNil reports whether a node is nil, including an interface holding a nil
// node pointer. It only inspects the value; it never constructs a node.
func IsNil(n Node) bool {
	if n == nil {
		return true
	}
	v := reflect.ValueOf(n)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

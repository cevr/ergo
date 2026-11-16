package check

// The operations of the built-in slice and Map types (PRIM, owner
// 2026-11-16): xs.Append(x), m.Get(k), m.Put(k, v), m.Delete(k) and
// m.Take(k). They belong to the language, the same in every program, as
// + belongs to int: the evaluator, the footprint, the value rule and both
// lowerings read which operation a call is from CollOpOf, the one place
// that names them, and never from a package path or a function's name.
// The value rule's in-place-when-dead choice stays the general ownership
// rule (lower values.go), never keyed on the operation.

// CollOp is an operation of the built-in slice or Map type, or CollNone.
type CollOp uint8

const (
	CollNone   CollOp = iota
	CollAppend        // xs.Append(x): xs with x added at the end
	CollGet           // m.Get(k): Some of k's value, or None
	CollPut           // m.Put(k, v): m with k set to v
	CollDelete        // m.Delete(k): m without k
	CollTake          // m.Take(k): k's value as an Option, and m without k
)

// collOps are the operations by the type they are on (D-D8: reached only
// by the dot form on exactly an unnamed slice or Map) and their name.
var collOps = map[collKey]CollOp{
	{name: "Append"}:              CollAppend,
	{name: "Get", isMap: true}:    CollGet,
	{name: "Put", isMap: true}:    CollPut,
	{name: "Delete", isMap: true}: CollDelete,
	{name: "Take", isMap: true}:   CollTake,
}

// CollOpOf is the operation of the built-in slice or Map type that f
// declares, or CollNone for every other function.
func CollOpOf(f *Func) CollOp {
	if f == nil {
		return CollNone
	}
	return f.collOp
}

// String is the operation's dot-form name.
func (op CollOp) String() string {
	for k, o := range collOps {
		if o == op {
			return k.name
		}
	}
	return "none"
}

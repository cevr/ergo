package check

// E2b round 3 review M1 and M2: unsafe, reflect, a module package's
// function, an interface method declared in another package, and a
// reflective decoder. Each is visible only at its import.

import (
	"encoding/json" // want `import "encoding/json" is not in the manifest's reviewed import allow-list`
	"reflect"       // want `reflect import permits unrecorded construction`
	"unsafe"        // want `unsafe import permits unrecorded construction`

	"ergo.local/prototype/internal/sketch/e2bdup"   // want `import "ergo.local/prototype/internal/sketch/e2bdup" is not in the manifest's`
	"ergo.local/prototype/internal/sketch/e2bmaker" // want `import "ergo.local/prototype/internal/sketch/e2bmaker" is not in the manifest's`
	"ergo.local/prototype/internal/sketch/syntax"
)

func unsafeCopied(s *syntax.VarSpec) syntax.Node {
	return unsafe.SliceData(append([]syntax.VarSpec(nil), unsafe.Slice(s, 1)...)) // want `append copies VarSpec node values outside made and clone \(in unsafeCopied\)`
}

var _ = reflect.TypeOf

func dupCopied(s *syntax.VarSpec) syntax.Node { return e2bdup.Dup(s) }

func makerCopied(m e2bmaker.Maker, s *syntax.VarSpec) syntax.Node { return m.Make(s) }

func decoded() syntax.Node {
	var v *syntax.VarSpec
	_ = json.Unmarshal([]byte("{}"), &v)
	return v
}

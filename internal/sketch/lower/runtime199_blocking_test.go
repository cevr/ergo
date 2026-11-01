package lower

import (
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"testing"
)

// Binding emission is a separate seam from runtime cancellation. New raw
// runtime calls must use runtimeCall, even when no fixture reaches them.
// The exceptions are formatting, collection intrinsics and frame plumbing;
// none is a user-visible blocking binding.
func TestRuntime199BlockingOwner(t *testing.T) {
	helpers := map[string]bool{
		"Append": true, "Delete": true, "NewScope": true, "Site": true,
		"RegisterCalls": true, "TextBool": true, "TextDuration": true,
		"TextInt": true, "TextUint": true, "TextFloat": true, "TextList": true,
		"Main": true, "Println": true, "End": true,
	}
	call := regexp.MustCompile(`\.([A-Za-z][A-Za-z0-9_]*)\(`)
	i1LowerSource(t, func(owner string, node ast.Node) {
		if owner == "runtimeCall" {
			return
		}
		usesRuntime := false
		var literals []string
		ast.Inspect(node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if s, ok := n.Fun.(*ast.SelectorExpr); ok {
					usesRuntime = usesRuntime || s.Sel.Name == "rt"
					if s.Sel.Name == "hiddenArgs" {
						t.Errorf("%s emits hidden binding arguments outside runtimeCall", owner)
					}
				}
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					value, err := strconv.Unquote(n.Value)
					if err != nil {
						t.Fatal(err)
					}
					literals = append(literals, value)
				}
			}
			return true
		})
		if usesRuntime {
			for _, text := range literals {
				for _, m := range call.FindAllStringSubmatch(text, -1) {
					if !helpers[m[1]] {
						t.Errorf("%s emits runtime binding %s outside runtimeCall", owner, m[1])
					}
				}
			}
		}
	})
}

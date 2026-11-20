package lower

import (
	"strings"
	"testing"
)

// TestLawBodyAppendsInPlace is the smallest repro of the FD3a slow law: a
// law whose body appends to its own fresh local in a loop lowered every
// Append with its source clipped (valueSource, V3), so each append copied
// the whole slice and the loop was quadratic (100 cases over n <= 20000:
// 8.3e9 elements copied, 61 s, against 0.5 s without the copies). The law
// body had no value facts: lawDecl never ran the value analysis that
// testDecl runs. It counts the copy sites in the lowered law body, which
// must be as many as in the same body lowered as a test: none. Mutant:
// law-values-not-analyzed.
func TestLawBodyAppendsInPlace(t *testing.T) {
	const src = `law CatalogHasN(n int where 1 <= n && n <= 20000) {
	let mut catalog = []string{}
	for i in 0..n {
		catalog = catalog.Append("x\(i)")
	}
	len(catalog) == n
}

test CatalogAtOneSize {
	let n = 556
	let mut catalog = []string{}
	for i in 0..n {
		catalog = catalog.Append("x\(i)")
	}
	assert len(catalog) == n
}
`
	code := lowerTestBuild(t, "lawvalues", src).mainGo()
	copies := func(body string) int { return strings.Count(body, "Clip(") + strings.Count(body, "Clone(") }
	law, test := goFunc(t, code, "ergoLawCatalogHasN"), goFunc(t, code, "ergoTestCatalogAtOneSize")
	if lc, tc := copies(law), copies(test); lc != tc || lc != 0 {
		t.Errorf("the law body has %d copy sites and the same body as a test %d, want 0 and 0:\n%s\n%s", lc, tc, law, test)
	}
}

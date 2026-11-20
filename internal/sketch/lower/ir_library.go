package lower

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/token"
	"maps"
	"slices"
	"sort"
)

// LowerIRLibrary is LowerLibrary with the IR bodies (capstone S1a, §12
// Decided 5): the library's Go package name, its exported Go functions and
// its other declarations as LowerLibrary writes them, and each body the
// IR builder can express as an IR body in the IR file, as LowerIR does.
// The module is programModule's, so the derivative builds its root
// package with -ergoir as it builds a program's; the package's file is
// name.go. Its Go text is the IR's view of what the derivative compiled,
// printed by IRGoText: internal/experimental/irspike LibraryText. The report
// names the bodies that kept a Go body, and why.
func LowerIRLibrary(u Unit, name string) ([]File, IRReport, error) {
	if err := u.Validate(); err != nil {
		return nil, IRReport{}, err
	}
	if !token.IsIdentifier(name) || name == "main" {
		return nil, IRReport{}, fmt.Errorf("lower: %q is not a Go package name for a library", name)
	}
	l := newBuildLowerer(u, nil)
	l.lib = name
	l.ir = &irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}}
	code, err := l.program()
	if err != nil {
		return nil, IRReport{}, err
	}
	sort.Slice(l.ir.out.Funcs, func(i, j int) bool { return l.ir.out.Funcs[i].Name < l.ir.out.Funcs[j].Name })
	l.ir.out.V = 1
	for _, decl := range slices.Sorted(maps.Keys(l.ir.keep)) {
		code = append(code, []byte("\n"+decl+" // an import an IR body names\n")...)
	}
	data, err := json.Marshal(l.ir.out)
	if err != nil {
		return nil, IRReport{}, err
	}
	sum := sha256.Sum256(data)
	report := l.ir.report
	report.Hash = hex.EncodeToString(sum[:])
	sort.Strings(report.IR)
	files := []File{{Path: "go.mod", Data: goModFile(u.Go)}}
	if u.Go.Sum != "" {
		files = append(files, File{Path: "go.sum", Data: []byte(u.Go.Sum)})
	}
	files = append(files, File{Path: name + ".go", Data: code})
	files = append(files, l.assetFiles()...)
	return append(files, File{Path: IRFileName, Data: data}), report, nil
}

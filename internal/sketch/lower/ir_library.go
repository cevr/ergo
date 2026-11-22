package lower

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/token"
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
	// The program's IR lowering itself (lowerIRModule), with the library's
	// package (LIB-ON-IR): its int map rounds, its fallback refusal under
	// NoFallback, and its form and position checks, which a library lowered
	// by its own loop before skipped.
	u.lib = name
	files, _, mod, _, err := lowerIRModule(u, nil, false, false)
	if err != nil {
		return nil, IRReport{}, err
	}
	for i := range files {
		if files[i].Path == "main.go" {
			files[i].Path = name + ".go"
		}
	}
	data, err := json.Marshal(mod.out)
	if err != nil {
		return nil, IRReport{}, err
	}
	sum := sha256.Sum256(data)
	report := mod.IRReport
	report.Hash = hex.EncodeToString(sum[:])
	return append(files, File{Path: IRFileName, Data: data}), report, nil
}

// LibraryModule is the Go module `ergo lib` writes for the library that
// LowerIRLibrary lowered from u into files (LIB-ON-IR): go.mod for the Go
// module path module, go.sum, the asset files, and text, the IR bodies
// printed as Go (irspike LibraryText), as name.go. The IR file is not in
// it: a Go program builds the library from its text with a stock go, as it
// built LowerLibrary's. Runtime's files go beside it, as for LowerLibrary.
func LibraryModule(u Unit, files []File, module, name string, text []byte) []File {
	out := []File{{Path: "go.mod", Data: goModFileAt(module, u.Go)}}
	for _, f := range files {
		switch f.Path {
		case "go.mod", IRFileName:
		case name + ".go":
			out = append(out, File{Path: f.Path, Data: text})
		default:
			out = append(out, f) // go.sum and the assets
		}
	}
	return out
}

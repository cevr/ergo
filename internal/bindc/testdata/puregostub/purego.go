// Package purego is a stub of github.com/ebitengine/purego v0.11.3 for the
// in-gate type check of the generated Go packages (TestGeneratedGoTypechecks).
// It holds only the API the generated code uses, with the upstream
// signatures, copied from these files of v0.11.3:
//
//   - dlfcn.go: Dlopen, Dlsym, Dlclose;
//   - dlfcn_linux.go: RTLD_NOW, RTLD_LOCAL (dlfcn_darwin.go has other
//     values of the same untyped constants);
//   - func.go: RegisterFunc.
//
// Every body panics: the stub is type-checked, never run. TestLive builds
// the same packages against the real module (testdata/purego.sum).
package purego

const (
	RTLD_NOW   = 0x00002 // dlfcn_linux.go
	RTLD_LOCAL = 0x00000 // dlfcn_linux.go
)

// Dlopen is dlfcn.go's Dlopen.
func Dlopen(path string, mode int) (uintptr, error) { panic("purego stub") }

// Dlsym is dlfcn.go's Dlsym.
func Dlsym(handle uintptr, name string) (uintptr, error) { panic("purego stub") }

// Dlclose is dlfcn.go's Dlclose.
func Dlclose(handle uintptr) error { panic("purego stub") }

// RegisterFunc is func.go's RegisterFunc.
func RegisterFunc(fptr any, cfn uintptr) { panic("purego stub") }

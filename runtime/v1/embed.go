package v1

import (
	"embed"
	"io/fs"

	"ergo.local/prototype/runtime/rt"
)

// FS is an immutable embedded tree. It exposes no writer and opens no host
// file. Its names are io/fs names rooted at the declaring source package.
// Methods keep the operation-type task ABI so the value also implements
// fs.Files; immutable reads never use that task and cannot reach the host.
type FS struct{ files fs.FS }

// EmbeddedFS wraps a Go embed tree after lowering has removed its private
// asset namespace. The native control is embed.FS plus io/fs.Sub.
func EmbeddedFS(files embed.FS, prefix string) *FS {
	view, err := fs.Sub(files, prefix)
	if err != nil {
		panic("invalid checked embed prefix: " + err.Error())
	}
	return &FS{files: view}
}

// NewEmptyFS is the body of embed.NewEmpty.
func NewEmptyFS() *FS { return &FS{files: embed.FS{}} }

func (f *FS) ReadFile(_ *rt.Task, name string) (string, error) {
	data, err := fs.ReadFile(f.files, name)
	if err != nil {
		return "", NotFound{Path: name}
	}
	return string(data), nil
}

func (f *FS) ReadDir(_ *rt.Task, name string) ([]string, error) {
	entries, err := fs.ReadDir(f.files, name)
	if err != nil {
		return nil, NotFound{Path: name}
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}

func (f *FS) Stat(_ *rt.Task, name string) (Info, error) {
	info, err := fs.Stat(f.files, name)
	if err != nil {
		return Info{}, NotFound{Path: name}
	}
	return Info{Name: info.Name(), Size: int(info.Size()), Dir: info.IsDir(), Perm: int(info.Mode().Perm())}, nil
}

# Files in the binary

The package declaration names its build-time inputs:

```go
import embed
import fs

let Help = embedded[string]("help.txt")
let Logo = embedded[[]byte]("logo.bin")
let Assets = embedded[embed.FS]("assets/*")

func Page(name string) string fails fs.NotFound | fs.Denied | fs.Failed needs fs.Files {
	fs.Files.ReadFile(name)
}
```

The entry binds `fs.Files` to `Assets`. A test binds the same contract to `fstest.Memory`. An invalid or missing pattern is a positioned check error, not a Go build error. The emitted module uses Go's `//go:embed`, never a Go literal of the payload. `embedded` is predeclared language behavior; `embed` is an ordinary standard-library package.

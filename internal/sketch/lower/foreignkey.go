package lower

import (
	"path"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/toolchain"
	runtimefiles "ergo.local/prototype/runtime"
)

// Foreign keys (LAWFFI r2 §3.5, LFL; owner q192 to q195). A ledger entry
// whose law reaches a foreign function, the Go target of an `extern go`
// (a binding's or a user's), pins its evidence to the version of the Go
// code behind it: the digest takes each target's foreign key, so a
// toolchain change, a go.sum bump of the module that holds the function,
// or an edit of Go code a runtime or trusted function reaches makes the
// entry stale, and an edit anywhere else does not. The Ergo side needs no
// key of its own: the digest already covers the tokens of every Ergo
// declaration the law reaches, which is the content hash of every body
// both sides reach (q195).
//
//	Go's standard library, a builtin    go <toolchain>         shown go1.27.4
//	a required module                    sum <its go.sum line>  shown <module>@<version>
//	runtime/v1 and its runtime imports   closure <hash>         shown <import path>@<12 hex>
//	a local replacement, go.mod's own    closure <hash>         shown <import path>@<12 hex>
//	none of these                        unknown <path>         shown <import path>@unknown
//
// A closure key is the hash of the declarations the function reaches
// (goclosure.go), widened to the package's files where the walk cannot
// see; a package's shown hash is over the keys of its targets the entry
// reaches. The platform is not keyed: the ledger is a function of the
// module's sources (LAWFFI r2 §3.5, §7 risk 4). The sources are the files
// go build compiles for the target (sketchfront's readGoDir), so a
// package whose files a build constraint chooses, such as a vector kernel
// and its stock twin, is keyed by the twin that is built (U1 SIMD-SCAN).
// A function of module A
// that calls module B is keyed by A's go.sum line only: B's version
// reaches the key only through A's own requirements.

// ForeignKey is a target's key: Key goes into the digest; Shown, or, when
// Group is set, Group and a hash of the keys of the group's targets an
// entry reaches, into the ledger line's `with` field, so a stale entry can
// say what changed. Build is the build configuration the key depends on
// (ForeignKeySource.Variants), "" when every variant gives the same key.
type ForeignKey struct{ Key, Shown, Group, Build string }

// ForeignKeys maps an `extern go` target to its key. A nil ForeignKeys
// keys nothing: the digest is the Ergo declarations' alone.
type ForeignKeys func(target string) ForeignKey

// ShippedForeignKeys are the keys of the packages the compiler ships (std
// and its ledger): the pinned toolchain and the runtime, read as the host
// compiles it, against every other supported platform; std links no Go
// module.
var ShippedForeignKeys = HostForeignKeySource(toolchain.Version).Keys()

// HostForeignKeySource is the source of the toolchain's and the runtime's
// keys for a build of the host's platform with no experiments, each other
// supported platform its variant.
func HostForeignKeySource(toolchain string) ForeignKeySource {
	host := GoTarget{GOOS: goruntime.GOOS, GOARCH: goruntime.GOARCH}
	s := ForeignKeySource{Toolchain: toolchain, Target: &host}
	for _, p := range gobind.Platforms {
		if p.GOOS != host.GOOS || p.GOARCH != host.GOARCH {
			s.Variants = append(s.Variants, BuildVariant{Tag: host.GOOS + "/" + host.GOARCH, Target: GoTarget{GOOS: p.GOOS, GOARCH: p.GOARCH}})
		}
	}
	return s
}

// ExternPath is the Go import path of an `extern go` target: hash/crc32
// for hash/crc32.ChecksumIEEE, net/url for net/url.URL.Hostname, math for
// math.Pi, and "" for a builtin (append), which Go's toolchain defines.
func ExternPath(target string) string {
	slash := strings.LastIndex(target, "/")
	dot := strings.Index(target[slash+1:], ".")
	if dot < 0 {
		return ""
	}
	return target[:slash+1+dot]
}

// ForeignKeySource is what the keys are read from: the toolchain's name,
// the program's Go module (go.mod's requirements and replacements, each
// local folder absolute, and go.sum), and the Go files of a folder
// (sketchfront reads them; nil when it cannot).
type ForeignKeySource struct {
	Toolchain string
	Module    GoModule
	ReadDir   func(dir string) []GoSourceFile
	// Variants are the other builds the program's Go may be compiled
	// in, each with the tag that names this build apart from it
	// (LAW-BUILD-KEY): sketchfront gives the stock build, tagged with the
	// experiments (goexperiment.simd), and each other supported platform
	// with the same experiments, tagged with this build's platform
	// (linux/amd64). A local package's key names, sorted and joined by
	// commas, the tag of each variant whose closure of the function
	// differs, so a ledger entry over a vector kernel, or over a
	// kern_linux.go and kern_darwin.go pair, is keyed by the build that
	// gave it, and one over code every build compiles alike is not.
	Variants []BuildVariant
	// Always are the tags every key of Go code the variants compare names,
	// whatever the comparison finds: the experiments of this build
	// (goexperiment.simd; LEDGER-EXP-KEY). A build with experiments
	// never shares a key with the stock build, even where the two compile
	// one closure, so no two builds read one entry by accident and the
	// writer can tell whose an entry is (DropSuperseded).
	Always []string
	// Target is this build (LAW-BUILD-KEY r3): the runtime's embedded
	// files are read as it compiles them, and as each variant's Target
	// does, so a runtime function with a body per platform
	// (exchange_linux.go, exchange_darwin.go) is keyed by the target's and
	// names its platform. nil reads every file of the runtime, as before.
	Target *GoTarget
	// worlds are the Go packages the keys have read, one world per local
	// module: a world's loader reads only its module's folders (LF2m r2:
	// one world, made at the first key, read only the first module's, so
	// a second local module's function was keyed unread).
	worlds map[string]*goWorld
}

// Keys is s's ForeignKeys, each target's key computed once.
func (s ForeignKeySource) Keys() ForeignKeys {
	s.worlds = nil
	var mu sync.Mutex
	memo := map[string]ForeignKey{}
	return func(target string) ForeignKey {
		mu.Lock()
		defer mu.Unlock()
		if k, ok := memo[target]; ok {
			return k
		}
		k := s.key(target)
		memo[target] = k
		return k
	}
}

// module is the module of s that holds the import path p ("" for none),
// and the folder that holds p when the module is replaced by one.
func (s ForeignKeySource) module(p string) (mod, version, dir string, repl *GoReplace) {
	for _, m := range s.Module.Modules {
		if (p == m || strings.HasPrefix(p, m+"/")) && len(m) > len(mod) {
			mod = m
		}
	}
	if mod == "" {
		return "", "", "", nil
	}
	for _, r := range s.Module.Require {
		if r.Path == mod {
			version = r.Version
		}
	}
	for i, r := range s.Module.Replace {
		if r.Old == mod && (r.OldVersion == version || r.OldVersion == "" && repl == nil) {
			repl = &s.Module.Replace[i]
		}
	}
	if repl != nil && (strings.HasPrefix(repl.New, "/") || strings.HasPrefix(repl.New, ".")) {
		dir = path.Join(repl.New, strings.TrimPrefix(p, mod))
	}
	return mod, version, dir, repl
}

func (s *ForeignKeySource) key(target string) ForeignKey {
	p := ExternPath(target)
	name := strings.TrimPrefix(target, p+".")
	if p == "" {
		name = target
	}
	if rel, ok := strings.CutPrefix(p, runtimefiles.Module+"/"); ok && strings.HasPrefix(rel, "runtime/") && s.Target != nil {
		// The runtime is the compiler's own Go, with no contract that
		// every platform runs one body (LAW-BUILD-KEY r3): its key is the
		// closure the target compiles, and names the build of each
		// variant whose closure differs.
		memo := s.Target.ID()
		for _, v := range s.Variants {
			memo += "|" + v.Tag + "=" + v.Target.ID()
		}
		memo += "|" + target
		if k, ok := runtimeKeys.Load(memo); ok {
			return k.(ForeignKey)
		}
		c := runtimeWorldFor(*s.Target).closure(p, name)
		k := ForeignKey{Key: "closure " + c.Hash, Group: rel}
		var tags []string
		for _, v := range s.Variants {
			if vc := runtimeWorldFor(v.Target).closure(p, name); vc.Hash != c.Hash && !slices.Contains(tags, v.Tag) {
				tags = append(tags, v.Tag)
			}
		}
		k.Build = s.buildOf(tags)
		runtimeKeys.Store(memo, k)
		return k
	}
	if rel, ok := strings.CutPrefix(p, runtimefiles.Module+"/"); ok && strings.HasPrefix(rel, "runtime/") {
		if k, ok := runtimeKeys.Load(target); ok {
			return k.(ForeignKey)
		}
		k := ForeignKey{Key: "closure " + runtimeWorld().closure(p, name).Hash, Group: rel}
		runtimeKeys.Store(target, k)
		return k
	}
	mod, version, dir, repl := s.module(p)
	if mod == "" && (p == "" || !strings.Contains(strings.Split(p, "/")[0], ".")) {
		// Go's standard library: a module path with no dot in its first
		// element (go.mod's own, such as app) was matched above. Its
		// key names no build: a verdict over Go standard library code
		// holds on every platform of the same Go version (interop.md,
		// Trusted Go; LAW-BUILD-KEY r3), as the toolchain that compiles
		// every emitted program is trusted to.
		return ForeignKey{Key: "go " + s.Toolchain, Shown: s.Toolchain}
	}
	if mod == "" {
		return ForeignKey{Key: "unknown " + p, Shown: p + "@unknown", Build: s.everyTag()}
	}
	if dir != "" {
		c := s.world(mod, s.ReadDir, "", s.Target).closure(p, name)
		if c.Unloaded {
			return ForeignKey{Key: "unread " + p, Shown: p + "@unread", Build: s.everyTag()}
		}
		k := ForeignKey{Key: "closure " + c.Hash, Group: p}
		var tags []string
		for i, v := range s.Variants {
			if vc := s.world(mod, v.Read, strconv.Itoa(i)+" ", &v.Target).closure(p, name); (vc.Unloaded || vc.Hash != c.Hash) && !slices.Contains(tags, v.Tag) {
				tags = append(tags, v.Tag)
			}
		}
		k.Build = s.buildOf(tags)
		return k
	}
	if repl != nil {
		mod, version = repl.New, repl.NewVersion
	}
	line := "none " + mod + " " + version
	for _, l := range strings.Split(s.Module.Sum, "\n") {
		if f := strings.Fields(l); len(f) == 3 && f[0] == mod && f[1] == version {
			line = strings.Join(f, " ")
		}
	}
	// A required module from go.sum is third-party Go with no contract
	// that every build runs one body, and the keys do not read it: its
	// key names every build tag (LAW-BUILD-KEY r3), so a verdict given
	// in one build is read only in that build.
	return ForeignKey{Key: "sum " + line, Shown: mod + "@" + version, Build: s.everyTag()}
}

// everyTag is the tag of every variant, sorted and joined by commas: the
// build part of a key whose code the keys cannot compare between builds.
func (s *ForeignKeySource) everyTag() string {
	var tags []string
	for _, v := range s.Variants {
		if !slices.Contains(tags, v.Tag) {
			tags = append(tags, v.Tag)
		}
	}
	return s.buildOf(tags)
}

// buildOf is a key's build part: the tags of the variants that differ and
// the Always tags, each once, sorted and joined by commas.
func (s *ForeignKeySource) buildOf(tags []string) string {
	return UnionBuild(strings.Join(tags, ","), strings.Join(s.Always, ","))
}

// UnionBuild is the build part naming every tag of the build parts bs,
// each once, sorted: an entry reaching several packages names each
// build any of them depends on.
func UnionBuild(bs ...string) string {
	var tags []string
	for _, b := range bs {
		for _, tag := range strings.Split(b, ",") {
			if tag != "" && !slices.Contains(tags, tag) {
				tags = append(tags, tag)
			}
		}
	}
	slices.Sort(tags)
	return strings.Join(tags, ",")
}

// BuildVariant is another build of the program's Go: Read reads a
// folder's files as that build compiles them, Target is that build (the
// runtime's files are read as it compiles them), and Tag names this build
// apart from it.
type BuildVariant struct {
	Tag    string
	Read   func(dir string) []GoSourceFile
	Target GoTarget
}

// world is the Go packages of the local module mod as read reads its
// folders, one world per module and reader (tag tells the readers apart).
func (s *ForeignKeySource) world(mod string, read func(dir string) []GoSourceFile, tag string, target *GoTarget) *goWorld {
	if s.worlds == nil {
		s.worlds = map[string]*goWorld{}
	}
	name := tag + mod
	w := s.worlds[name]
	if w == nil {
		w = newGoWorld(func(ip string) ([]GoSourceFile, bool) {
			if read == nil {
				return nil, false
			}
			m, _, d, _ := s.module(ip)
			if m != mod || d == "" {
				return nil, false
			}
			files := read(d)
			return files, files != nil
		})
		if target != nil {
			w.std = stdTypesOf(*target)
		}
		s.worlds[name] = w
	}
	return w
}

// short is the first 12 hex digits of a hash, its shown form.
func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// runtimeKeys are the keys of the runtime's targets, which the compiler
// embeds: each is computed once per process.
var runtimeKeys sync.Map // target -> ForeignKey

// runtimeWorlds are the runtime's Go packages as each target compiles
// them, by GoTarget.ID, each parsed and typed once.
var runtimeWorlds sync.Map // target ID -> *goWorld

func runtimeWorldFor(t GoTarget) *goWorld {
	if w, ok := runtimeWorlds.Load(t.ID()); ok {
		return w.(*goWorld)
	}
	nw := newGoWorld(func(ip string) ([]GoSourceFile, bool) {
		all, ok := runtimeGoFiles(ip)
		if !ok {
			return nil, false
		}
		var out []GoSourceFile
		for _, f := range all {
			if t.Compiles(f.Name, f.Data) {
				out = append(out, f)
			}
		}
		return out, true
	})
	nw.std = stdTypesOf(t)
	w, _ := runtimeWorlds.LoadOrStore(t.ID(), nw)
	return w.(*goWorld)
}

// runtimeWorld is the runtime's Go packages, every file of each, parsed
// and typed once.
var runtimeWorld = sync.OnceValue(func() *goWorld { return newGoWorld(runtimeGoFiles) })

var runtimeGoCache sync.Map // import path -> []GoSourceFile

// runtimeGoFiles are the embedded files of the runtime package at the
// import path ip, and false for a path that is not one.
func runtimeGoFiles(ip string) ([]GoSourceFile, bool) {
	if v, ok := runtimeGoCache.Load(ip); ok {
		files := v.([]GoSourceFile)
		return files, files != nil
	}
	var out []GoSourceFile
	if rel, ok := strings.CutPrefix(ip, runtimefiles.Module+"/"); ok && strings.HasPrefix(rel, "runtime/") {
		if files, err := runtimefiles.Files(rel); err == nil {
			for _, f := range files {
				if path.Dir(f.Path) == rel {
					out = append(out, GoSourceFile{Name: path.Base(f.Path), Data: f.Data})
				}
			}
		}
	}
	runtimeGoCache.Store(ip, out)
	return out, out != nil
}

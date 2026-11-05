package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/internal/testinputs/testenv"
	"ergo.local/prototype/scripts/gate/replay"
)

// The whole-gate key (package replay) covers every input of every step:
// the gate executable, the checkout (HEAD, the index and every path under
// the root), the environment a step can read, the gate's settings and the
// host fingerprint. Package replay composes it; this file collects the facts
// that need programs or the host: Git's view of the checkout, the tool
// fingerprints of the declared host programs, and the host facts.

// hostPrograms are the declared host programs: every program a step starts
// (argv[0], and the programs its shell script names), every program this
// package's code and tests start, and the C compilers go build runs for
// cgo. The whole-gate key fingerprints each one (toolVersion), so a change
// to any of them, or one removed from PATH, changes the key. A program a
// step or test starts that is not here fails TestHostProgramsDeclared.
var hostPrograms = []string{
	// The toolchain, and the gofmt PATH finds, which may be another file
	// than the toolchain's.
	"go", "gofmt", "gcc", "clang",
	// Steps' programs and their scripts'.
	"git", "sh", "env", "cmp", "mv", "mkdir", "dirname", "mktemp", "grep",
	// The gate's own: the systemd scope, the RSS watch, darwin's memory size.
	"systemd-run", "systemctl", "ps", "true", "sysctl",
	// The host tests' and their scripts'.
	"nohup", "head", "python3", "seq", "setsid", "sleep", "ls", "chmod",
}

// readPhysicalMemory reads the host's physical memory in bytes, or 0 when
// it cannot. The host fingerprint calls it at every collection; a test
// replaces it to inject an unknown or a changing size.
var readPhysicalMemory = totalMemory

// hostFacts are the host fingerprint, with the installation roots the tool
// fingerprints cover (where a link may lead and stay keyed), and the facts
// the gate could not read.
type hostFacts struct {
	facts    []replay.Fact
	roots    []string
	problems []string
}

// collectHost reads the host fingerprint. mechanism is the memory mechanism
// this gate runs under, or would run under (mechanismFact).
func (g *gate) collectHost(mechanism string) hostFacts {
	var h hostFacts
	var mu sync.Mutex
	add := func(name, value string) {
		mu.Lock()
		h.facts = append(h.facts, replay.Fact{Name: name, Value: value})
		mu.Unlock()
	}
	problem := func(format string, args ...any) {
		mu.Lock()
		h.problems = append(h.problems, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	// A fresh fingerprint cache: nothing is remembered from the run's start.
	fresh := &gate{root: g.root, cacheDir: g.cacheDir}
	programs := hostPrograms
	if g.programs != nil {
		programs = g.programs
	}
	var wg sync.WaitGroup
	for _, program := range programs {
		wg.Go(func() {
			value := fresh.toolVersion(program)
			if reason, ok := strings.CutPrefix(value, "unlocated\x00"); ok {
				reason, _, _ = strings.Cut(reason, "\x00")
				problem("program %s cannot be fingerprinted: %s", program, reason)
				value = "unlocated"
			} else if _, after, ok := strings.Cut(value, "\x00unlocated\x00"); ok {
				reason, _, _ := strings.Cut(after, "\x00")
				problem("program %s cannot be fingerprinted: %s", program, reason)
				value = "unlocated"
			}
			add("program "+program, value)
		})
	}
	add("goos", runtime.GOOS)
	add("goarch", runtime.GOARCH)
	add("cpus", strconv.Itoa(runtime.NumCPU()))
	// Read afresh at each collection, so the end key sees a change; an
	// unknown size is never a value that matches (spec Do 2).
	if total := readPhysicalMemory(); total > 0 {
		add("physical memory", strconv.FormatInt(total, 10))
	} else {
		problem("the physical memory cannot be read")
		add("physical memory", "unknown")
	}
	add("memory mechanism", mechanism)
	if budget, err := currentBudget(); err != nil {
		problem("the memory budget: %v", err)
	} else {
		add("memory budget", fmt.Sprintf("%d %d %d", budget.step, budget.gate, budget.host))
	}
	if mechanism == mechanismScope {
		bound, err := sliceProperty()
		if err != nil {
			problem("the bound of the gates' slice cannot be read: %v", err)
		}
		add("slice bound", bound)
	}
	if release, version, err := kernelFacts(); err != nil {
		problem("the kernel cannot be read: %v", err)
	} else {
		add("kernel release", release)
		add("kernel version", version)
	}
	cgroups, problems := cgroupFacts()
	for _, fact := range cgroups {
		add(fact[0], fact[1])
	}
	for _, p := range problems {
		problem("%s", p)
	}
	for _, place := range fileSystemPlaces(g.root, g.cacheDir) {
		kind, err := fileSystemType(place[1])
		if err != nil {
			problem("the file system of %s (%s) cannot be read: %v", place[0], place[1], err)
		}
		add("file system of "+place[0], place[1]+" "+kind)
	}
	add("user id", strconv.Itoa(os.Getuid()))
	if limit, err := openFileLimit(); err != nil {
		problem("RLIMIT_NOFILE cannot be read: %v", err)
	} else {
		add("RLIMIT_NOFILE", limit)
	}
	wg.Wait()
	h.roots = fresh.installationRoots()
	return h
}

// fileSystemPlaces are the directories whose file system a step's verdict
// can depend on: the checkout, the temporary directory (each step's TMPDIR
// is below it), GOTMPDIR, GOCACHE, the gate cache and its output spools.
func fileSystemPlaces(root, cache string) [][2]string {
	places := [][2]string{{"the checkout", root}, {"the temporary directory", os.TempDir()}}
	if dir := os.Getenv("GOTMPDIR"); dir != "" {
		places = append(places, [2]string{"GOTMPDIR", dir})
	}
	goCache := os.Getenv("GOCACHE")
	if goCache == "" {
		if dir, err := os.UserCacheDir(); err == nil {
			goCache = filepath.Join(dir, "go-build")
		}
	}
	if goCache != "" && goCache != "off" {
		places = append(places, [2]string{"GOCACHE", goCache})
	}
	spools := filepath.Join(os.TempDir(), "ergo-gate", "output")
	if cache != "" {
		places = append(places, [2]string{"the gate cache", cache})
		spools = filepath.Join(cache, "output")
	}
	return append(places, [2]string{"the output spools", spools})
}

// checkoutFacts are Git's view of the checkout: HEAD, the index entries and
// the class of every path.
type checkoutFacts struct {
	head     string
	index    []string
	classify replay.Classifier
}

// collectCheckout asks Git for HEAD, the index (mode, blob id, stage and
// path of every entry) and the untracked files. A path the index names is
// tracked; one Git lists as untracked, or below an untracked directory it
// lists whole (an embedded repository), is untracked; every other path is
// ignored.
func (g *gate) collectCheckout() (checkoutFacts, error) {
	var head, index, others []byte
	var headErr, indexErr, othersErr error
	var wg sync.WaitGroup
	wg.Go(func() { head, headErr = g.git("rev-parse", "HEAD") })
	wg.Go(func() { index, indexErr = g.git("ls-files", "-s", "-z") })
	wg.Go(func() { others, othersErr = g.git("ls-files", "-z", "--others", "--exclude-standard") })
	wg.Wait()
	for _, err := range []error{headErr, indexErr, othersErr} {
		if err != nil {
			return checkoutFacts{}, err
		}
	}
	facts := checkoutFacts{head: strings.TrimSpace(string(head))}
	tracked := map[string]bool{}
	for _, entry := range strings.Split(string(index), "\x00") {
		if entry == "" {
			continue
		}
		facts.index = append(facts.index, entry)
		if _, name, ok := strings.Cut(entry, "\t"); ok {
			tracked[name] = true
		}
	}
	untracked := map[string]bool{}
	var untrackedDirs []string
	for _, name := range strings.Split(string(others), "\x00") {
		switch {
		case name == "":
		case strings.HasSuffix(name, "/"):
			untrackedDirs = append(untrackedDirs, name)
		default:
			untracked[name] = true
		}
	}
	facts.classify = func(name string) replay.Class {
		switch {
		case tracked[name]:
			return replay.Tracked
		case untracked[name]:
			return replay.Untracked
		case slices.ContainsFunc(untrackedDirs, func(dir string) bool { return strings.HasPrefix(name, dir) }):
			return replay.Untracked
		}
		return replay.Ignored
	}
	return facts, nil
}

// collectionTime is the time of a collection, against which Walk finds
// recent stat-keyed paths. A test sets it ahead to step past RecentWindow
// without waiting.
var collectionTime = time.Now

// collectFacts collects every fact of the whole-gate key at this moment:
// the checkout and the host at once, then the links against the keyed
// roots the host fingerprint found.
func (g *gate) collectFacts(mechanism string) (*replay.Facts, error) {
	var host hostFacts
	var wg sync.WaitGroup
	wg.Go(func() { host = g.collectHost(mechanism) })
	facts, err := g.checkoutFacts(collectionTime())
	wg.Wait()
	if err != nil {
		return nil, err
	}
	facts.Host = host.facts
	facts.Unreplayable = append(facts.Unreplayable, host.problems...)
	links, err := replay.NewLinks(g.root, host.roots)
	if err != nil {
		return nil, err
	}
	facts.Unreplayable = append(facts.Unreplayable, links.FollowLinks(facts.Entries)...)
	return facts, nil
}

// checkoutFacts collects the facts of the checkout, the gate and the
// environment, without the host fingerprint and the links.
func (g *gate) checkoutFacts(now time.Time) (*replay.Facts, error) {
	checkout, err := g.collectCheckout()
	if err != nil {
		return nil, err
	}
	entries, recent, unreplayable, err := replay.Walk(g.root, checkout.classify, now)
	if err != nil {
		return nil, fmt.Errorf("walk the checkout: %w", err)
	}
	executable, err := executableDigest()
	if err != nil {
		unreplayable = append(unreplayable, "the gate executable cannot be read: "+err.Error())
	}
	return &replay.Facts{
		Executable:   executable,
		Root:         g.root,
		Head:         checkout.head,
		Index:        checkout.index,
		Entries:      entries,
		Environment:  declaredEnvironment(),
		Settings:     gateSettings(g.cacheDir),
		Unreplayable: unreplayable,
		Recent:       recent,
	}, nil
}

// executableDigest is the SHA-256 of the running gate executable.
func executableDigest() (string, error) {
	name, err := os.Executable()
	if err != nil {
		return "", err
	}
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// declaredEnvironment is everything a step can read from its environment:
// each variable of testenv.Names as the caller gives it to steps, set or
// not (keyedEnvironment), but the tool digest. The gate never takes the tool
// digest from its caller: it derives it from these values and the tool
// fingerprints. The uncached default in GOFLAGS (stepEnvironment), a step's
// own variables (Env) and its TMPDIR follow from the gate's code and the
// checkout root. TestReceivedVariablesAreKeyed checks that every variable a
// step or a tool query receives is keyed here or derived.
func declaredEnvironment() []replay.Variable {
	environment := keyedEnvironment()
	var variables []replay.Variable
	for _, name := range testenv.Names {
		if name == testenv.ToolDigest {
			continue
		}
		value, set := environmentVariable(environment, name)
		variables = append(variables, replay.Variable{Name: name, Set: set, Value: value})
	}
	return variables
}

// gateSettings are the gate's own settings: every ERGO_GATE_* variable,
// XDG_CACHE_HOME and the cache directory.
func gateSettings(cacheDir string) []replay.Variable {
	var settings []replay.Variable
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ERGO_GATE_") {
			settings = append(settings, replay.Variable{Name: name, Set: true, Value: value})
		}
	}
	value, set := os.LookupEnv("XDG_CACHE_HOME")
	settings = append(settings, replay.Variable{Name: "XDG_CACHE_HOME", Set: set, Value: value})
	return append(settings, replay.Variable{Name: "cache directory", Set: cacheDir != "", Value: cacheDir})
}

// mechanismScope is the mechanism fact of a gate in its systemd scope.
const mechanismScope = "systemd scope"

// mechanismFact is the memory mechanism a gate runs its steps under: a gate
// in its scope, or one whose probe found a scope would start (probed), runs
// in the systemd scope; any other, under the RSS watch, for its reason.
func mechanismFact(scope string, probed bool, reason string) string {
	switch {
	case scope != "" || probed:
		return mechanismScope
	case os.Getenv(mechanismVariable) != "":
		return "rss, forced by " + mechanismVariable
	}
	return "rss: " + reason
}

// cString reads a NUL-terminated C string.
func cString[T int8 | uint8](field []T) string {
	var b strings.Builder
	for _, c := range field {
		if c == 0 {
			break
		}
		b.WriteByte(byte(c))
	}
	return b.String()
}

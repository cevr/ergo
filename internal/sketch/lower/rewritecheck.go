package lower

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lawcache"
)

// The build's evidence for rewrite laws (types.md, Laws 14; owner LR q1:
// the build checks each site instance by a cached generated Go check
// run). RewriteChecks lowers the run build to learn the laws and the site
// instances it asks for; lowers the test build of the same unit with
// those instances as cases of their own (RewriteInstance); and runs each
// asked law of it alone (--law). A law's run that ends ok and proved, with
// the instance among its cases, admits the law for that provider at that
// instance; a tested, refuted or skipped run, or an instance the check
// did not take, admits nothing (owner q115: proved only).
//
// The first lowering admits every law it asks for, so it learns the laws
// a lawful program uses. A refused law changes what the lowering asks for
// next (another law applies in its place, at other constants), so the
// lowering runs again with the verdicts, and asks the check for what it
// had not checked, up to rewriteCheckRounds checks; what is still
// unchecked then is refused. The caller builds and runs each check, and
// caches its output by the generated files, which hold every input of
// the verdict: the sources, the instances and the seed.

// RewriteCheckSeed is the seed the check run takes: the same files give
// the same verdicts.
const RewriteCheckSeed = "1"

// rewriteCheckRounds bounds the check runs of one build.
const rewriteCheckRounds = 3

// RefutedRewriteError is a reachable counterexample, not an unavailable
// optimization check. A build must preserve this refusal across its fallback.
type RefutedRewriteError struct{ Line string }

func (e *RefutedRewriteError) Error() string { return "rewrite law refuted: " + e.Line }

// RewriteChecks returns the evidence for u's run build. run builds the
// test files, runs each law title of laws with --seed RewriteCheckSeed
// --law TITLE, and returns their output. A unit with no rewrite law that
// a provider's run checks needs no check: its evidence admits nothing,
// and run is not called.
func RewriteChecks(u Unit, run func(files []File, laws []string) (string, error)) (RewriteEvidence, error) {
	return RewriteChecksCached(u, run, nil)
}

// RewriteChecksCached is RewriteChecks with the verdict store (lawcache;
// v1 audit 3, F7): a law whose check run's output at its key the store
// holds is not run again, and when no law is left to run, neither the
// check build nor run is. The key (checkRunKey) covers every input of the
// law's run in the check build; a run that printed a FAIL line is never
// stored. A nil store runs every law.
func RewriteChecksCached(u Unit, run func(files []File, laws []string) (string, error), store *lawcache.Store) (RewriteEvidence, error) {
	ev := &checkedEvidence{pkg: u.Package, ok: map[string]string{}, verdict: map[string]string{}, how: map[string]string{}, ran: map[*check.Law]bool{}, insts: map[string]bool{}}
	if len(newRewriter(u.Package, ev).rules) == 0 {
		return ev, nil
	}
	ev.optimistic = true
	var laws []*check.Law
	var insts []RewriteInstance
	for range rewriteCheckRounds {
		ev.asked, ev.askedInsts = nil, nil
		probe := u
		probe.Rewrite = ev
		if _, err := probeLower(probe); err != nil {
			return nil, err
		}
		if u.AllLaws && len(laws) == 0 {
			// Every law of the unit, whether a site asks for it or not.
			seen := map[*check.Law]bool{}
			for _, law := range ev.asked {
				seen[law] = true
			}
			for _, rules := range newRewriter(u.Package, nil).rules {
				for _, r := range rules {
					if !seen[r.law] {
						seen[r.law] = true
						ev.asked = append(ev.asked, r.law)
					}
				}
			}
			sort.Slice(ev.asked, func(i, j int) bool { return ev.asked[i].Title(u.Package) < ev.asked[j].Title(u.Package) })
		}
		if len(ev.asked)+len(ev.askedInsts) == 0 {
			break
		}
		laws, insts = append(laws, ev.asked...), append(insts, ev.askedInsts...)
		t := u
		t.Rewrite, t.Instances, t.Posts = nil, insts, nil
		for _, law := range laws {
			if law.Rewrite == nil {
				// A post law's fact holds after its operation from every
				// state, so its check starts from a drawn one.
				t.Posts = append(t.Posts, law)
			}
		}
		var titles []string
		keys := map[string]string{}
		var out string
		for _, law := range laws {
			title := law.Title(u.Package)
			if key, ok := checkRunKey(u.Package, law, insts, slices.Contains(t.Posts, law)); ok {
				if text, hit := store.Get(key); hit {
					out += text
					continue
				}
				keys[title] = key
			}
			titles = append(titles, title)
		}
		sort.Strings(titles)
		if len(titles) > 0 {
			files, err := LowerTests(t)
			if err != nil {
				return nil, err
			}
			ran, err := run(files, titles)
			if err != nil {
				return nil, err
			}
			for title, text := range outputByLaw(ran, titles) {
				if key, ok := keys[title]; ok {
					store.Put(key, text)
				}
			}
			out += ran
		}
		ev.optimistic = false
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "FAIL law ") && strings.Contains(line, "(reachable state)") {
				return nil, &RefutedRewriteError{Line: line}
			}
			rest, ok := strings.CutPrefix(line, "ok law ")
			label := "tested"
			if !ok {
				rest, ok = strings.CutPrefix(line, "skip law ")
				label = "skipped"
			}
			if !ok {
				continue
			}
			title, prov, _ := strings.Cut(rest, " for ")
			prov, how, _ := strings.Cut(prov, " ")
			if strings.HasPrefix(how, "(proved") {
				label = "proved"
			}
			key := evidenceKey(title, prov)
			if label != "skipped" {
				ev.ok[key] = label
			}
			ev.verdict[key] = label
			how = strings.TrimSuffix(strings.TrimPrefix(how, "("), ")")
			if _, after, cut := strings.Cut(how, ": "); cut && (strings.HasPrefix(how, "proved") || strings.HasPrefix(how, "not proved")) {
				how = after
			}
			ev.how[key] = how
		}
		for _, law := range laws {
			ev.ran[law] = true
		}
		for _, in := range insts {
			ev.insts[instanceKey(in)] = true
		}
		ev.instList = insts
	}
	ev.optimistic, ev.asked, ev.askedInsts = false, nil, nil
	return ev, nil
}

// checkRunKey is the store's key of the check run of law in the check
// build of pkg (lawcache): the law's title, each binding's label and
// TargetDigest, in order, the site instances of the law the build takes,
// in order, whether it runs as a post law, and the seed. A law that runs
// under a context has none: a context's declarations are not in the
// digest, so it runs every time.
func checkRunKey(pkg *check.Package, law *check.Law, insts []RewriteInstance, post bool) (string, bool) {
	parts := []string{"check run", law.Title(pkg), RewriteCheckSeed, strconv.FormatBool(post)}
	found := false
	for _, r := range pkg.LawRuns {
		if r.Law != law {
			continue
		}
		if len(r.Contexts) > 0 || found {
			return "", false
		}
		found = true
		for _, b := range r.Bindings {
			parts = append(parts, "binding", BindingLabel(pkg, law, b), bindingUnrun(b), TargetDigest(pkg, law, b))
		}
	}
	if !found {
		return "", false
	}
	for _, in := range insts {
		if in.Law == law {
			parts = append(parts, "instance", instanceKey(in))
		}
	}
	return lawcache.Key(parts...), true
}

// outputByLaw splits the output of the check runs of titles by law: the
// lines `ok law T …`, `skip law T…` and `FAIL law T …` are T's. Any other
// line (a summary, a replay) is no law's.
func outputByLaw(out string, titles []string) map[string]string {
	by := map[string]string{}
	for _, line := range strings.SplitAfter(out, "\n") {
		trimmed := strings.TrimSuffix(line, "\n")
		for _, title := range titles {
			for _, head := range []string{"ok law ", "skip law ", "FAIL law "} {
				rest, ok := strings.CutPrefix(trimmed, head+title)
				if ok && (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, ":")) {
					if !strings.HasSuffix(line, "\n") {
						line += "\n"
					}
					by[title] += line
				}
			}
		}
	}
	return by
}

// probeLower lowers the run build of u to learn what it asks for. A
// library (a std package) has no main, so its bodies are lowered with no
// entry.
func probeLower(u Unit) ([]byte, error) {
	for _, f := range u.Package.Funcs {
		if f.Name == "main" {
			_, err := Lower(u)
			return nil, err
		}
	}
	return lowerPackage(u, true)
}

// checkedEvidence is the verdicts of the check runs: ok by law and
// provider title, with the run's label (proved or tested), the laws they
// ran, and insts the site instances they took. It records what a lowering
// asks for that no run checked (asked, askedInsts), and admits that only
// while optimistic.
//
// Only proved evidence changes code (owner q115, 2026-11-11): a run that
// held over drawn cases is tested, which ergo test and the check report,
// and the build never acts on it. admitTested is the one exception, and
// only a test sets it: the engine's agreement property (TestRewriteAgrees)
// runs rewrites on tested laws, which no build does.
type checkedEvidence struct {
	pkg *check.Package
	ok  map[string]string
	// verdict and how are each run's verdict (proved, tested or
	// skipped) and its text, for the ledger (LedgerEntries).
	verdict, how map[string]string
	ran          map[*check.Law]bool
	insts        map[string]bool
	instList     []RewriteInstance
	optimistic   bool
	admitTested  bool
	asked        []*check.Law
	askedInsts   []RewriteInstance
}

// Admit admits law for prov at the site in when the law's check run for
// prov proved it and took the site; see checkedEvidence.
func (e *checkedEvidence) Admit(law *check.Law, prov *check.Named, in RewriteInstance) (bool, string) {
	key := evidenceKey(law.Title(e.pkg), provTitle(e.pkg, prov))
	checked := true
	if !e.ran[law] {
		checked = false
		if !e.lawAsked(law) {
			e.asked = append(e.asked, law)
		}
	}
	if constArgs(in) && !e.insts[instanceKey(in)] {
		checked = false
		if !e.instAsked(in) {
			e.askedInsts = append(e.askedInsts, in)
		}
	}
	switch {
	case !checked && e.optimistic:
		return true, ""
	case !checked:
		return false, "no check run of law " + key + " took this site"
	case e.ok[key] == "":
		return false, "the check run of law " + key + " did not pass"
	case e.ok[key] != "proved" && !e.admitTested:
		return false, "law " + key + " is tested, not proved: only proved laws change code"
	}
	return true, ""
}

func (e *checkedEvidence) lawAsked(law *check.Law) bool {
	for _, l := range e.asked {
		if l == law {
			return true
		}
	}
	return false
}

func (e *checkedEvidence) instAsked(in RewriteInstance) bool {
	k := instanceKey(in)
	for _, a := range e.askedInsts {
		if instanceKey(a) == k {
			return true
		}
	}
	return false
}

// evidenceKey keys a verdict by its law and its provider: evidence is per
// provider, so one lawful provider never admits a law for another.
func evidenceKey(law, prov string) string { return law + " for " + prov }

// provTitle is the provider's title in a report of pkg's test build
// (lowerer.providerTitle).
func provTitle(pkg *check.Package, p *check.Named) string {
	if q := p.Obj.Pkg; q != nil && q != pkg {
		return q.Name + "." + p.Obj.Name
	}
	return p.Obj.Name
}

func constArgs(in RewriteInstance) bool {
	for _, a := range in.Args {
		if a != nil {
			return true
		}
	}
	return false
}

// instanceKey names a site instance: its law and its constants.
func instanceKey(in RewriteInstance) string {
	var b strings.Builder
	b.WriteString(in.Law.Title(nil))
	for _, a := range in.Args {
		b.WriteString("|")
		if a != nil {
			b.WriteString(a.Kind().String() + ":" + a.ExactString())
		}
	}
	return b.String()
}

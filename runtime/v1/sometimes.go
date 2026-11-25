package v1

import "sync"

var lawSometimesState struct {
	mu    sync.Mutex
	sites [][2]string
	hits  []int
}

// LawSometimesBegin registers the static `sometimes` check sites of one law
// run before case 1 executes. Each entry is [site, exprText].
//
//ergo:wait none
func LawSometimesBegin(sites [][2]string) {
	lawSometimesState.mu.Lock()
	lawSometimesState.sites = sites
	lawSometimesState.hits = make([]int, len(sites))
	lawSometimesState.mu.Unlock()
}

// LawSometimesHit records one evaluation of the static `sometimes` site idx.
// When cond is true, the site's hit counter increments.
//
//ergo:wait none
func LawSometimesHit(idx int, cond bool) {
	if !cond {
		return
	}
	lawSometimesState.mu.Lock()
	if idx >= 0 && idx < len(lawSometimesState.hits) {
		lawSometimesState.hits[idx]++
	}
	lawSometimesState.mu.Unlock()
}

// LawSometimesEnd checks that every statically registered `sometimes` site was
// hit at least once and clears the active table. When any site has zero hits,
// it returns the first unhit site, its expression text, and ok=false.
//
//ergo:wait none
func LawSometimesEnd() (site, expr string, ok bool) {
	lawSometimesState.mu.Lock()
	defer func() {
		lawSometimesState.sites = nil
		lawSometimesState.hits = nil
		lawSometimesState.mu.Unlock()
	}()
	for i, s := range lawSometimesState.sites {
		if lawSometimesState.hits[i] == 0 {
			return s[0], s[1], false
		}
	}
	return "", "", true
}

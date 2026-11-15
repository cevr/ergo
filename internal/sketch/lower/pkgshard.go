package lower

import (
	"go/ast"
	"hash/fnv"
	"path"
	"slices"
	"strconv"
)

// Declaration shards (test-speed FT9, the fallback of plan §3.5;
// docs/tooling.md, "Package split"). The package split compiles each
// Ergo package as one Go package, so a body edit compiles the whole
// package again. Sharding cuts a big package of the program into Go
// packages below it, so Go's action cache per package becomes a cache per
// shard, and a body edit compiles one shard: Rust's reuse per codegen unit,
// with no change to Go's compiler.
//
// A shard is a run of strongly connected components of the package's
// declaration groups (a set of mutually recursive declarations stays
// together, and a type with its methods is already one group), in an
// order with every callee before its callers, so a shard only names the
// shards before it and the shards have no import cycle (law
// ShardsAreAcyclic). The cuts are content-defined: a shard ends after a
// component whose first name hashes to 0 mod shardCutOne once it holds
// min bytes, or at max bytes, so an inserted or a removed declaration
// moves only the cuts near it (law ShardsLocalUnderInsert); and a shard is
// named by its first name, not by its index. The last shard stays in the
// package itself. In the root, the pinned groups (main, every init, an
// embedded asset) and every group that reaches one stay in the root,
// which nothing imports (law InitsKeepTheirOrder stands).
//
// The rest is the split's: the names that cross a shard get the
// exported spelling, and each shard gets its own IR file.
//
// Prior art: Rust's codegen units (rustc_monomorphize partitioning),
// which group functions into units by a stable hash and reuse a unit
// whose inputs did not change; Zig and Roc reuse per declaration in the
// compiler. Gleam, ReScript and Elixir emit one host module per source
// module and leave the host's unit as is.

// shardOpts are the sizes of the cut: a shard ends at a hashed cut once it
// holds min bytes of declarations, and at the latest at max bytes.
type shardOpts struct{ min, max int }

// devShards are the shard sizes of a test build: a 144 KB test program
// makes about eight shards, each of which pays the compile's process
// start and imports (58 M to 107 M instructions, plan §3.5).
var devShards = &shardOpts{min: 8 << 10, max: 32 << 10}

// shardCutOne is the inverse rate of a hashed cut: one component in four
// may end a shard.
const shardCutOne = 4

// shardDir is the folder of the shards of every package.
const shardDir = "shard"

// shard cuts every program package of p holding at least o.min bytes into
// shards, after placement: it appends the shards to p.pkgs and moves their
// groups. name is a group's first package-scope name, size its bytes.
// It returns the number of shards made.
func (p *splitPlan) shard(groups []splitGroup, o *shardOpts, name func(g int) string, size func(g int) int) int {
	made := 0
	for k, n := 1, len(p.pkgs); k < n; k++ {
		if p.pkgs[k].std {
			continue
		}
		var members []int
		total := 0
		for g := range groups {
			if groups[g].pkg == k {
				members = append(members, g)
				total += size(g)
			}
		}
		if total < o.min {
			continue
		}
		stay := map[int]bool{}
		if k == 1 {
			stay = rootReach(groups, members)
		}
		var free []int
		for _, g := range members {
			if !stay[g] {
				free = append(free, g)
			}
		}
		runs := cutShards(groups, free, o, name, size)
		if len(runs) < 2 {
			continue
		}
		// The last run stays in the package: it names the others.
		runs = runs[:len(runs)-1]
		taken := map[string]bool{}
		first := len(p.pkgs)
		for _, run := range runs {
			stem := "s" + hex8(name(run[0]))
			id := stem
			for i := 2; taken[id]; i++ {
				id = stem + "_" + strconv.Itoa(i)
			}
			taken[id] = true
			dir := path.Join(shardDir, "root", id)
			if p.pkgs[k].dir != "" {
				dir = path.Join(shardDir, p.pkgs[k].dir, id)
			}
			sees := map[int]bool{}
			for j := range p.pkgs[k].sees {
				sees[j] = true
			}
			for j := first; j < len(p.pkgs); j++ {
				sees[j] = true
			}
			s := len(p.pkgs)
			p.pkgs = append(p.pkgs, splitPkg{ergo: p.pkgs[k].ergo, dir: dir, name: id, sees: sees})
			for _, g := range run {
				groups[g].pkg = s
			}
			made++
		}
		// Who sees the package sees its shards.
		for j := 1; j < first; j++ {
			if j == k || p.pkgs[j].sees[k] {
				for s := first; s < len(p.pkgs); s++ {
					p.pkgs[j].sees[s] = true
				}
			}
		}
	}
	return made
}

// rootReach is the root's groups that stay in the root: the pinned ones,
// and every group of members that references one of them.
func rootReach(groups []splitGroup, members []int) map[int]bool {
	in := map[int]bool{}
	for _, g := range members {
		in[g] = true
	}
	users := map[int][]int{}
	for _, u := range members {
		for _, v := range groups[u].refs {
			if in[v] {
				users[v] = append(users[v], u)
			}
		}
	}
	stay := map[int]bool{}
	var work []int
	for _, g := range members {
		if groups[g].pin {
			stay[g] = true
			work = append(work, g)
		}
	}
	for len(work) > 0 {
		v := work[len(work)-1]
		work = work[:len(work)-1]
		for _, u := range users[v] {
			if !stay[u] {
				stay[u] = true
				work = append(work, u)
			}
		}
	}
	return stay
}

// cutShards orders the components of members callees first and cuts them
// into runs of groups (each run's first group is its first component's
// first group).
func cutShards(groups []splitGroup, members []int, o *shardOpts, name func(g int) string, size func(g int) int) [][]int {
	comps := components(groups, members)
	var runs [][]int
	var run []int
	bytes := 0
	for _, c := range comps {
		run = append(run, c...)
		for _, g := range c {
			bytes += size(g)
		}
		if bytes >= o.max || bytes >= o.min && fnv32(name(c[0]))%shardCutOne == 0 {
			runs = append(runs, run)
			run, bytes = nil, 0
		}
	}
	if len(run) > 0 {
		runs = append(runs, run)
	}
	return runs
}

// components are the strongly connected components of members over their
// references among members (Tarjan), each sorted by group index, in an
// order with every component after the components it references, ties
// by the first group index.
func components(groups []splitGroup, members []int) [][]int {
	in := map[int]bool{}
	for _, g := range members {
		in[g] = true
	}
	index := map[int]int{}
	low := map[int]int{}
	onStack := map[int]bool{}
	var stack []int
	comp := map[int]int{}
	var comps [][]int
	next := 0
	var visit func(v int)
	visit = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range groups[v].refs {
			if !in[w] {
				continue
			}
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var c []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp[w] = len(comps)
				c = append(c, w)
				if w == v {
					break
				}
			}
			slices.Sort(c)
			comps = append(comps, c)
		}
	}
	for _, g := range members {
		if _, seen := index[g]; !seen {
			visit(g)
		}
	}
	// Kahn's order over the components: a component is ready when every
	// component it references is placed; the ready one with the lowest
	// first group goes next.
	deps := make([]map[int]bool, len(comps))
	users := make([][]int, len(comps))
	for c := range comps {
		deps[c] = map[int]bool{}
	}
	for c, gs := range comps {
		for _, g := range gs {
			for _, w := range groups[g].refs {
				if d, ok := comp[w]; ok && in[w] && d != c && !deps[c][d] {
					deps[c][d] = true
					users[d] = append(users[d], c)
				}
			}
		}
	}
	waiting := make([]int, len(comps))
	var ready []int
	for c := range comps {
		waiting[c] = len(deps[c])
		if waiting[c] == 0 {
			ready = append(ready, c)
		}
	}
	var out [][]int
	for len(ready) > 0 {
		i := 0
		for j := range ready {
			if comps[ready[j]][0] < comps[ready[i]][0] {
				i = j
			}
		}
		c := ready[i]
		ready = slices.Delete(ready, i, i+1)
		out = append(out, comps[c])
		for _, u := range users[c] {
			if waiting[u]--; waiting[u] == 0 {
				ready = append(ready, u)
			}
		}
	}
	return out
}

// declName is the first package-scope name declaration d (the i-th of
// main.go) declares: a function's, a type's, a value's; "_i" for a
// declaration of no name, which then names its shard by position.
func declName(d ast.Decl, i int) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				return s.Name.Name
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if n.Name != "_" {
						return n.Name
					}
				}
			}
		}
	}
	return "_" + strconv.Itoa(i)
}

// fnv32 is the FNV-1a hash of s.
func fnv32(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// hex8 is fnv32(s) in eight hex digits.
func hex8(s string) string {
	const digits = "0123456789abcdef"
	v := fnv32(s)
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = digits[v&15]
		v >>= 4
	}
	return string(b)
}

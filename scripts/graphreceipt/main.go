// Command graphreceipt is GRAPH-STD's receipt: it runs one query of
// `ergo graph` over a rows file (`ergo graph export --json`) either in
// Ergo (std codegraph, through the linked package internal/linked/graph,
// as `ergo graph` runs it) or in hand-written Go (the control: the same
// answer from the same rows, decoded with encoding/json, a parent table
// walked once, maps keyed by integers), or only decodes the rows (the
// floor's read: every query reads them whole). Count each with perf:
//
//	go build -o /tmp/gr ./scripts/graphreceipt
//	perf stat -e instructions:u /tmp/gr -mode ergo -query hidden rows.json
//
// It prints the answer's size, so the three modes can be compared.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	lgraph "ergo.local/prototype/internal/linked/graph"
)

type node struct {
	Kind   string `json:"kind"`
	Sub    string `json:"sub"`
	Parent int    `json:"parent"`
	Name   string `json:"name"`
}

type edge struct {
	Kind string `json:"kind"`
	How  string `json:"how"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

type rows struct {
	Version int    `json:"version"`
	Nodes   []node `json:"nodes"`
	Edges   []edge `json:"edges"`
}

func main() {
	mode := flag.String("mode", "ergo", "ergo, columns, go or decode")
	query := flag.String("query", "hidden", "hidden, filecycles, or none (columns mode: the handover alone)")
	flag.Parse()
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var out string
	switch *mode {
	case "ergo":
		res, err := lgraph.Run(context.Background(), queryArgs(*query), string(data), "")
		if err != nil || res[0] != "0" {
			fmt.Fprintln(os.Stderr, err, res)
			os.Exit(1)
		}
		out = res[1]
	case "columns":
		// As the ergo binary runs a query since GRAPH-FAST: the rows go
		// over as columns, not JSON. They are decoded here as the decode
		// mode does (the floor's read) and laid out as AppendColumns does,
		// so columns minus decode is the handover and the Ergo query.
		var r rows
		if err := json.Unmarshal(data, &r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		strs, ints := columns(&r)
		res, err := lgraph.RunColumns(context.Background(), queryArgs(*query), strs, ints)
		// The query none is the handover alone: RunColumns reads the
		// columns into rows, then stops at the missing command (status 2).
		if err != nil || (res[0] != "0" && *query != "none") {
			fmt.Fprintln(os.Stderr, err, res)
			os.Exit(1)
		}
		out = res[1]
	case "decode":
		var r rows
		if err := json.Unmarshal(data, &r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		out = fmt.Sprint(len(r.Nodes), len(r.Edges))
	case "go":
		var r rows
		if err := json.Unmarshal(data, &r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var v any
		if *query == "filecycles" {
			v = fileCycles(&r)
		} else {
			v = hidden(&r)
		}
		b, _ := json.Marshal(v)
		out = string(b) + "\n"
	}
	fmt.Println(len(out))
}

func queryArgs(query string) []string {
	if query == "none" {
		return []string{}
	}
	if query == "filecycles" {
		return []string{"cycles", "--level", "file", "--json"}
	}
	return []string{"hidden", "--json"}
}

// columns is the rows in RunColumns' layout (graphrows.Table.AppendColumns);
// the digest and a task's subject, which these queries do not read, are
// left out (empty, -1), so the decode stays the control's.
func columns(r *rows) ([]string, []int) {
	ints := make([]int, 0, 2+2*len(r.Nodes)+2*len(r.Edges))
	strs := make([]string, 0, 4*len(r.Nodes)+2*len(r.Edges))
	ints = append(ints, len(r.Nodes), len(r.Edges))
	for _, n := range r.Nodes {
		ints = append(ints, n.Parent, -1)
		strs = append(strs, n.Kind, n.Sub, n.Name, "")
	}
	for _, e := range r.Edges {
		ints = append(ints, e.From, e.To)
		strs = append(strs, e.Kind, e.How)
	}
	return strs, ints
}

// up is, by node, its package and its file (-1 for none), each walked once.
func up(r *rows) (pkg, file []int) {
	pkg = make([]int, len(r.Nodes))
	file = make([]int, len(r.Nodes))
	for i, n := range r.Nodes {
		pkg[i], file[i] = -1, -1
		switch n.Kind {
		case "package":
			pkg[i] = i
		case "file":
			file[i] = i
		}
		if p := n.Parent; p >= 0 && p < i {
			if pkg[i] < 0 {
				pkg[i] = pkg[p]
			}
			if file[i] < 0 {
				file[i] = file[p]
			}
		}
	}
	return pkg, file
}

func name(r *rows, pkg []int, id int) string {
	n := r.Nodes[id]
	if n.Kind != "decl" || pkg[id] < 0 {
		return n.Name
	}
	return r.Nodes[pkg[id]].Name + "." + n.Name
}

type hiddenOut struct {
	From, To, Package, Via, How string
}

// hidden is codegraph.Hidden's answer, as ergo graph hidden --json prints it.
func hidden(r *rows) []hiddenOut {
	pkg, _ := up(r)
	width := len(r.Nodes)
	imported := map[int]bool{}
	reexport := map[int]int{}
	for _, e := range r.Edges {
		switch e.Kind {
		case "imports":
			imported[pkg[e.From]*width+e.To] = true
		case "reexports":
			reexport[e.From] = e.To
		}
	}
	out := []hiddenOut{}
	for _, e := range r.Edges {
		if e.Kind != "uses" {
			continue
		}
		a, c := pkg[e.From], pkg[e.To]
		if a >= 0 && c >= 0 && a != c && !imported[a*width+c] {
			out = append(out, hiddenOut{name(r, pkg, e.From), name(r, pkg, e.To), r.Nodes[c].Name, "", e.How})
		}
		if t, ok := reexport[e.To]; ok {
			if d := pkg[t]; a >= 0 && d >= 0 && a != d && !imported[a*width+d] {
				out = append(out, hiddenOut{name(r, pkg, e.From), name(r, pkg, t), r.Nodes[d].Name, name(r, pkg, e.To), e.How})
			}
		}
	}
	return out
}

type cycleOut struct {
	Nodes, Path []string
}

// fileCycles is codegraph.Cycles at the file level: Tarjan's components
// of the use graph between files of one package, each with a shortest
// closed path through its least file (breadth first).
func fileCycles(r *rows) []cycleOut {
	pkg, file := up(r)
	succ := map[int][]int{}
	seen := map[[2]int]bool{}
	for _, e := range r.Edges {
		if e.Kind != "uses" {
			continue
		}
		a, b := file[e.From], file[e.To]
		if a < 0 || b < 0 || a == b || pkg[a] != pkg[b] || seen[[2]int{a, b}] {
			continue
		}
		seen[[2]int{a, b}] = true
		succ[a] = append(succ[a], b)
	}
	index := map[int]int{}
	low := map[int]int{}
	on := map[int]bool{}
	var stack []int
	var comps [][]int
	var visit func(v int)
	visit = func(v int) {
		index[v], low[v] = len(index), len(index)
		stack = append(stack, v)
		on[v] = true
		for _, w := range succ[v] {
			if _, ok := index[w]; !ok {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var c []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				c = append(c, w)
				if w == v {
					break
				}
			}
			if len(c) > 1 {
				sort.Ints(c)
				comps = append(comps, c)
			}
		}
	}
	for i, n := range r.Nodes {
		if n.Kind == "file" {
			if _, ok := index[i]; !ok {
				visit(i)
			}
		}
	}
	out := []cycleOut{}
	for _, c := range comps {
		in := map[int]bool{}
		for _, v := range c {
			in[v] = true
		}
		first := c[0]
		parent := map[int]int{}
		queue := []int{}
		for _, m := range succ[first] {
			if in[m] {
				if _, ok := parent[m]; !ok {
					parent[m] = first
					queue = append(queue, m)
				}
			}
		}
		for h := 0; h < len(queue); h++ {
			v := queue[h]
			if v == first {
				break
			}
			for _, m := range succ[v] {
				if _, ok := parent[m]; !ok && in[m] {
					parent[m] = v
					queue = append(queue, m)
				}
			}
		}
		path := []int{first}
		for v := parent[first]; v != first; v = parent[v] {
			path = append(path, v)
		}
		path = append(path, first)
		for i, j := 1, len(path)-2; i < j; i, j = i+1, j-1 {
			path[i], path[j] = path[j], path[i]
		}
		var ns, ps []string
		for _, v := range c {
			ns = append(ns, r.Nodes[v].Name)
		}
		for _, v := range path {
			ps = append(ps, r.Nodes[v].Name)
		}
		out = append(out, cycleOut{ns, ps})
	}
	return out
}

package graphrows

import (
	"encoding/json"
	"fmt"
)

// The JSON of the rows (`ergo graph export --json`; docs/spec/graph.md):
// one object per node and per edge, by id, with names written out and
// kinds by name. Every row has every field: a missing node (a root's
// parent, a non-task's subject, no position's file) is -1, and the
// position fields file, offset, line and column are 0 without one.
// No key reads a position.
type (
	jsonTable struct {
		Version int        `json:"version"`
		Nodes   []jsonNode `json:"nodes"`
		Edges   []jsonEdge `json:"edges"`
	}
	jsonNode struct {
		ID      uint32 `json:"id"`
		Kind    string `json:"kind"`
		Sub     string `json:"sub"`
		Parent  int64  `json:"parent"`
		Name    string `json:"name"`
		Digest  string `json:"digest"`
		Subject int64  `json:"subject"`
		Lines   uint32 `json:"lines"`
		jsonPos
	}
	jsonEdge struct {
		Kind string `json:"kind"`
		How  string `json:"how"`
		From uint32 `json:"from"`
		To   uint32 `json:"to"`
		jsonPos
	}
	jsonPos struct {
		File   int64  `json:"file"`
		Offset uint32 `json:"offset"`
		Line   uint32 `json:"line"`
		Column uint32 `json:"column"`
	}
)

// id is a node id, or -1 for NoNode.
func id(n uint32) int64 {
	if n == NoNode {
		return -1
	}
	return int64(n)
}

func posJSON(p Pos) jsonPos {
	if p.File == NoNode {
		return jsonPos{File: -1}
	}
	return jsonPos{File: id(p.File), Offset: p.Off, Line: p.Line, Column: p.Col}
}

// SubName is the name of a node's Sub.
func (t *Table) SubName(n Node) string {
	switch n.Kind {
	case Decl:
		return DeclKind(n.Sub).String()
	case Task:
		return TaskKind(n.Sub).String()
	case Package:
		if n.Sub == PackageGo {
			return "go"
		}
	}
	return ""
}

// MarshalJSON is the rows' JSON, edges in the canonical order (Sorted).
func (t *Table) MarshalJSON() ([]byte, error) {
	out := jsonTable{Version: Version, Nodes: make([]jsonNode, len(t.Nodes)), Edges: make([]jsonEdge, 0, len(t.Edges))}
	for i, n := range t.Nodes {
		var lines uint32
		if i < len(t.NodeLines) {
			lines = t.NodeLines[i]
		}
		out.Nodes[i] = jsonNode{ID: uint32(i), Kind: n.Kind.String(), Sub: t.SubName(n), Parent: id(n.Parent), Name: t.Str(n.Name), Digest: t.Str(n.Digest), Subject: id(n.Subject), Lines: lines, jsonPos: posJSON(t.NodeAt[i])}
	}
	for _, i := range t.Sorted() {
		e := t.Edges[i]
		je := jsonEdge{Kind: e.Kind.String(), From: e.From, To: e.To, jsonPos: posJSON(t.EdgeAt[i])}
		if e.Kind == Uses {
			je.How = e.How.String()
		}
		out.Edges = append(out.Edges, je)
	}
	return json.Marshal(out)
}

// AppendColumns appends the rows to strs and ints in the column form the
// linked `ergo graph` reads with no JSON (internal/linked/graph,
// RunColumns): to ints the node and edge counts, each node's parent and
// subject (-1 for none), each edge's from and to; to strs each node's
// kind, sub-kind, name and digest, each edge's kind and how. Edges are in
// the canonical order, as MarshalJSON writes them; positions are left out.
func (t *Table) AppendColumns(strs []string, ints []int) ([]string, []int) {
	order := t.Sorted()
	ints = append(ints, len(t.Nodes), len(order))
	for i, n := range t.Nodes {
		var lines int
		if i < len(t.NodeLines) {
			lines = int(t.NodeLines[i])
		}
		ints = append(ints, int(id(n.Parent)), int(id(n.Subject)), lines)
		strs = append(strs, n.Kind.String(), t.SubName(n), t.Str(n.Name), t.Str(n.Digest))
	}
	for _, i := range order {
		e := t.Edges[i]
		ints = append(ints, int(e.From), int(e.To))
		how := ""
		if e.Kind == Uses {
			how = e.How.String()
		}
		strs = append(strs, e.Kind.String(), how)
	}
	return strs, ints
}

// Text is a line per node and per edge, the edges in canonical order,
// for a reader: `n<ID> <kind>[/<sub>] <name> [in n<parent>]` and
// `<kind>[/<how>] n<from> -> n<to> [file:line:column]`.
func (t *Table) Text() string {
	var b []byte
	for i, n := range t.Nodes {
		b = fmt.Appendf(b, "n%d %s", i, n.Kind)
		if s := t.SubName(n); s != "" {
			b = fmt.Appendf(b, "/%s", s)
		}
		b = fmt.Appendf(b, " %s", t.Str(n.Name))
		if n.Parent != NoNode {
			b = fmt.Appendf(b, " in n%d", n.Parent)
		}
		b = append(b, '\n')
	}
	for _, i := range t.Sorted() {
		e := t.Edges[i]
		b = fmt.Appendf(b, "%s", e.Kind)
		if e.Kind == Uses {
			b = fmt.Appendf(b, "/%s", e.How)
		}
		b = fmt.Appendf(b, " n%d -> n%d", e.From, e.To)
		if at := t.EdgeAt[i]; at.File != NoNode {
			b = fmt.Appendf(b, " %s:%d:%d", t.Str(t.Nodes[at.File].Name), at.Line, at.Col)
		}
		b = append(b, '\n')
	}
	return string(b)
}

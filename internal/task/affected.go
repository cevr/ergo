package task

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"
)

// Base is the tree an affected set is taken against (a commit, read from
// Git's objects; a folder in tests), as the working tree differs from it.
type Base interface {
	// Changed reports whether the file at path (slash-separated, from the
	// root) holds other bytes, or exists on one side only.
	Changed(path string) bool
	// Listing is folder dir's listing digest at the base, as listing
	// gives it for the working tree (Entries' digest; missing for none).
	Listing(dir string) (string, error)
}

// Entry is one name a folder holds, with whether it is a folder.
type Entry struct {
	Name string
	Dir  bool
}

// ListingDigest is the listing digest of a folder holding entries, which
// are sorted by name, as listing computes it for a folder on disk.
func ListingDigest(entries []Entry) string {
	h := sha256.New()
	for _, e := range entries {
		mark := "f"
		if e.Dir {
			mark = "d"
		}
		io.WriteString(h, mark+e.Name+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Affect is a node whose key at the base differs from its key in the
// working tree, with the reason.
type Affect struct {
	Name, Why string
}

// Affected are the nodes a regeneration of the working tree may run that
// one of the base would not (build.md, Generators, --affected): a node
// with no last run of this tree; one whose last run read a file that
// changed, or listed a folder whose names changed; one whose output
// changed (a hand edit, or a regeneration the base lacks); and every node
// that runs after one of these (its edges), since what it reads is
// regenerated. They come in the graph's order. Each key is taken over
// the node's last run in this tree, so the answer is as fresh as the
// last `ergo task gen`; the environment is the host's on both sides.
func (r *Runner) Affected(nodes []Node, base Base) ([]Affect, error) {
	g, err := r.Graph(nodes)
	if err != nil {
		return nil, err
	}
	why := make([]string, len(nodes))
	for i, n := range nodes {
		text, ok := r.Store.Get(r.lastKey(n))
		if !ok {
			why[i] = "no stored last run"
			continue
		}
		rec, ok := decodeRecord(text)
		if !ok {
			why[i] = "the stored last run does not read (an older format)"
			continue
		}
		why[i], err = r.moved(rec, base)
		if err != nil {
			return nil, err
		}
	}
	// What runs after an affected node is affected: its inputs are
	// regenerated. The order puts every edge's head first but in a
	// cycle, so the passes stop when nothing more is reached.
	for changed := true; changed; {
		changed = false
		for _, e := range g.Edges {
			if why[e[0]] != "" && why[e[1]] == "" {
				why[e[1]] = "runs after " + nodes[e[0]].Name
				changed = true
			}
		}
	}
	var out []Affect
	for _, i := range g.Order {
		if why[i] != "" {
			out = append(out, Affect{Name: nodes[i].Name, Why: why[i]})
		}
	}
	return out, nil
}

// moved is why a last run's key differs between base and the working
// tree, or "" when it does not: its first changed input, its first folder
// whose names moved, or its first changed output.
func (r *Runner) moved(rec record, base Base) (string, error) {
	for _, f := range rec.in.Files {
		if base.Changed(f) {
			return "input " + f + " changed", nil
		}
	}
	for _, d := range rec.in.Dirs {
		was, err := base.Listing(d)
		if err != nil {
			return "", err
		}
		now, err := listing(r.Root, d)
		if err != nil {
			return "", err
		}
		if was != now {
			return "the names in folder " + d + " changed", nil
		}
	}
	for _, o := range slices.Clone(rec.outs) {
		if base.Changed(o.path) {
			return "output " + o.path + " changed", nil
		}
	}
	return "", nil
}

package facts

// Ref is a site of one of several tables: Table is its index in the list
// the query read.
type Ref struct {
	Table int
	Site  Site
}

// References are the sites of the symbol with handle h in tables, table
// by table, each table's in its own order. Read counts the work: the
// handle-index probes and the rows read. A table that does not name h
// costs its probes only, so the read is the symbol's rows plus a
// logarithm per table (plan §2.9 budget 1).
func References(tables []*Table, h string) (refs []Ref, read int) {
	for ti, t := range tables {
		id, probes, ok := t.Lookup(h)
		read += probes
		if !ok {
			continue
		}
		for _, s := range t.Refs(id) {
			read++
			refs = append(refs, Ref{Table: ti, Site: s})
		}
	}
	return refs, read
}

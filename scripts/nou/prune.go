package main

import (
	"bufio"
	"fmt"
	"io"
)

// pruneLedger receives a checked subset: the inventory rejects new IDs
// and increased counts before previewing or changing the worktree.
func pruneLedger(name string, write bool, out io.Writer, rows map[siteID]allowance, have map[siteID]int) error {
	if !write {
		return writeLedger(out, rows, have)
	}
	removed := map[siteID]int{}
	total := 0
	for id, row := range rows {
		if n := row.count - have[id]; n > 0 {
			removed[id] = n
			total += n
		}
	}
	if total != 0 {
		if err := replaceLedger(name, rows, have); err != nil {
			return err
		}
	}
	b := bufio.NewWriter(out)
	for _, id := range sortedSiteIDs(removed) {
		row := rows[id]
		fmt.Fprintf(b, "removed\t%s\t%s\t%s\t%s\t%d\n", row.family, row.scope, id.file, id.anchor, removed[id])
	}
	fmt.Fprintf(b, "%d legacy sites removed\n", total)
	return b.Flush()
}

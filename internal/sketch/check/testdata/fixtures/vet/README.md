# vet

The vet class (VET-CHECK, from VET-GAP's findings): source that builds and
is always wrong, which Go leaves to go vet and staticcheck. Each rule is
decided by facts the checker has, so it is an error. Each program gets
exactly its markers; the lines with no marker are the rule's controls.

| Program | Rule | Mutant |
| --- | --- | --- |
| self-assignment.ergo | `x = x`, a field or an element of one storing its own value (S110; go vet assign) | vet-self-assign-none, vet-self-assign-any-index |

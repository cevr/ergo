# union-flat

A union may list a union of its own package, meaning that union's variants;
a union converts to every union of its package that holds each of its
variants; and a match arm `case A as a` takes a listed union's variants and
binds them as an `A` (types.md, Unions 1 and Assignability; DECL-VALUES
Design B (b), unit UNION-FLAT).

| Program | Rule | Mutants |
| --- | --- | --- |
| lists-union.ergo | the listing, widening by subset, `case A as a`, a variant reached twice counted once (`AB1`, `AB2`), a failure union nested in a union and a generic union | uflat-dup-variant |
| refused.ergo | another package's union (wrap text), a listing cycle, an arm after `case A as a` that it covers, `case A` alone not total, a union that is no subset, a nested sub-union test | uflat-other-pkg, uflat-narrow-total |

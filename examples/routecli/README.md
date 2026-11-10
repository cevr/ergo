# Route planner commands

`ergo run examples/routecli/cmd/routecli -- route rule add demo -n= -p 0 -t first --target second`

Three command levels produce a `routing.Command`, a closed union. The CLI
package owns its handles; routing owns its data and display. The binary
alone chooses the host process provider. An absent note and priority stay
`None`; explicit empty text and zero stay `Some`. Targets retain input order.
`--version` is declared on the root, and help names each command level.

This example plans input only; it does not change host routes.

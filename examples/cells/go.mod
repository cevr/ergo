module ergo.local/prototype/examples/cells

go 1.27

toolchain go1.27.4

// The two GUI examples whose views and scene scripts the cell host plays
// (package scenes, cmd/cells): Ergo modules this one requires (grammar.md,
// Required modules), from their folders in this repository.
require (
	ergo.local/prototype/examples/countergui v0.0.0
	ergo.local/prototype/examples/todogui v0.0.0
)

replace ergo.local/prototype/examples/countergui => ../counter-gui

replace ergo.local/prototype/examples/todogui => ../todo-gui

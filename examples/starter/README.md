# Project starter

`ergo run examples/starter/cmd/starter` asks at a terminal for a project's
name, license, features and whether it is a Git repository, with std
`prompt` (a line, a select, a multi-select and a confirm), and prints the
files the plan makes. `--name` and the other flags give the plan with no
questions.

`plan` is the domain: unions for the license and the features, a `Plan`
record, and laws over the files a plan makes. `cli` reads a plan from
flags. `terminal` asks for one; with no terminal it fails
`prompt.NotInteractive`, and `main` turns that into "give --name" (exit 2).
So a run with no person cannot block on a question: the failure is in the
row, and the binary decides.

The cases run with no terminal: one gives the plan by flags, one shows the
refusal. The prompts themselves are held by std prompt's laws and
`TestPromptLaws` over a scripted terminal.

It plans files only; it writes nothing.

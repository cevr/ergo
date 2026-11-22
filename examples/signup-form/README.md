# signup-form

A subscription form for a project's release notes: a name, an email address and a note. Each field is checked as it is typed; a well-formed address is also checked against the list of subscribers; a submit sends the subscription. The program plays a script of form events on the form's screen and shows the screen after each event.

Tag: `default`.

## How it works

- Package `subscribe` is the domain.
  - The valid values are types: `Name` is refined, `type Name string where len(it) >= 2`, so the where derives `ParseName` and the law `NameWhere`; `Email` is refined by a pattern, `type Email string where matches(it, "…")` (types.md, Refinements 11), with JavaScript's spaces spelled out in the class for `\s`; the compiler compiles the pattern to a Go matcher, and the where derives `ParseEmail` and the law `EmailWhere`. `EmailField` gives an empty text `Required` and a text the pattern refuses `Malformed`.
  - A field is `Field[T, W] { Text, Verdict }`, with `Verdict = Unchecked | Checking[T]{Value} | Valid[T]{Value} | Invalid[W]{Why}`. The reasons are types: `TooShort` for the name, `EmailWhy = Required | Malformed | Taken` for the email. `NameSaid` and `EmailSaid` give the texts.
  - The state `Form` is the three fields and the stage of the submit, `Stage = Editing | Sending | Sent{Text} | Refused`. The machine `Subscribing` has two kinds of work: a `Checking` email asks `Subscribers.Taken`, and a `Sending` form asks `Subscribers.Join`.
  - An edit of the name or the note is a `Stay`: it keeps the entry, so a check under way goes on. An edit of the email is an `Enter`: it opens a new entry, so a check of an older address ends, and the runner drops its answer.
  - A submit starts only when the name is valid or empty and the email has passed its check. During a submit the form takes no edit (see the ledger: one work per state).
  - `subscribe` declares the contract it needs, `Subscribers { Taken(e) bool, Join(s) fails JoinFailed }`, with the law `TakenIgnoresCase`.
- Package `memory` has two providers:
  - `List`: the three addresses already on the list. It refuses a subscription in the `.invalid` domain, which takes no mail.
  - `Slow`: answers as `List` does, after a wait that `main` gives it (a sleep on the script's clock).
- Package `screen` is the view:
  - the heading;
  - the three fields. Each is a `ui.Input` named by its label (its placeholder) in a `ui.Fieldset` whose description is what its check says: the error, or `Checking...` while the list checks the address;
  - the button, `Subscribe`, disabled until the form is `Ready`, and `Subscribing...` while a submit is under way;
  - the answer: a `Status` region with the welcome text, or an `Alert` region with the refusal.
  - Enter in the form (`ui.OnKey`) submits whatever the button shows, as a form's implicit submission does, and the step refuses a form that is not ready.
- Package `script` plays lines on `ui.NewTestHost`:
  - `name TEXT`, `email TEXT` and `note TEXT` fill that field;
  - `submit` clicks `Subscribe`;
  - `enter` presses Enter;
  - `wait MS` moves the clock, so `Slow` answers;
  - `stats` prints the work of the act before it.

  After each line it prints the screen: `LABEL<TEXT> (DESCRIPTION)`, `[button]` or `[button, disabled]`, and `status{…}` or `alert{…}`.
- `cmd/signup-form` is the only place that chooses the provider: `memory.Slow`, which waits 300 ms on the manual clock. That clock is also the test host's clock and the actor's Scheduler. The `Subscribers` context (`memory.List`) is where the contract's law runs.

## Laws

- `EditClearsThatFieldsError`: outside a submit, an edit sets its field to the field its text gives, an earlier error included, and touches nothing else; during a submit it changes nothing.
- `StaleCheckIsDropped`: an email edit opens a new entry, a name or note edit keeps it, and a check's answer when no check is under way changes nothing.
- `SubmitOnlyWhenAllValid`: over forms the user can reach, a submit starts exactly when the form is not sending, the name is valid or empty, and the email has passed its check.
- `EmailIsThePattern`: an address built of clean parts parses back; a space of JavaScript's `\s`, a second `@`, or a domain with no inner dot fails the where.
- `NameWhere` (derived from the where), `Subscribers.TakenIgnoresCase` (contract; runs for `memory.List`, and under the `Subscribers` context in `cmd/signup-form`).

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `scenes`: the nine scene tests, in order on one screen.
- `subscribe`: the flow: an ignored empty submit, a short and a long name, a malformed and an empty address, an address on the list (in another case), a new address, a submit and a second submit, then an empty name and a refused subscription.
- `invalid`: a short name blocks a checked address; six addresses the pattern refuses; a two-byte name, then a checked address, and the form is sent.
- `badscript`: an unknown line; the program says which line and exits 1.

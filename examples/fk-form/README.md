# fk-form

A subscription form for a project's release notes: a name, an email address and a note. Each field is checked as it is typed; a well-formed address is also checked against the list of subscribers; a submit sends the subscription. It is a port of Foldkit's `form` example (`conformance/upstream/foldkit-d21db423/examples/form`, MIT), stage a of FK5: the model, with no view yet. Upstream's form is a waitlist subscription; the port keeps its fields, rules and flow, with neutral texts. The program plays a script of form events against the form, run as an actor, and shows the form after each event.

Tag: `default`.

## How it works

- Package `subscribe` is the domain.
  - The valid values are types: `Name` is refined, `type Name string where len(it) >= 2`, so the where derives `ParseName` and the law `NameWhere`; `Email` has an unexported field, so only `ParseEmail` builds one. `ParseEmail` is upstream's pattern `^[^\s@]+@[^\s@]+\.[^\s@]+$`, written by hand, and fails `Required` or `Malformed`.
  - A field is `Field[T, W] { Text, Verdict }`, with `Verdict = Unchecked | Checking[T]{Value} | Valid[T]{Value} | Invalid[W]{Why}`. The reasons are types: `TooShort` for the name, `EmailWhy = Required | Malformed | Taken` for the email. `NameSaid` and `EmailSaid` give upstream's texts.
  - The state `Form` is the three fields and the stage of the submit, `Stage = Editing | Sending | Sent{Text} | Refused`. The machine `Subscribing` has two kinds of work: a `Checking` email asks `Subscribers.Taken`, and a `Sending` form asks `Subscribers.Join`.
  - An edit of the name or the note is a `Stay`: it keeps the entry, so a check under way goes on. An edit of the email is an `Enter`: it opens a new entry, so a check of an older address ends, and the runner drops its answer. Upstream carries the address in the answer and compares it with the field; here the old answer never arrives.
  - A submit starts only when the name is valid or empty (upstream's name is optional) and the email has passed its check. During a submit the form takes no edit (see the ledger: one work per state).
  - `subscribe` declares the contract it needs, `Subscribers { Taken(e) bool, Join(s) fails JoinFailed }`, with the law `TakenIgnoresCase`.
- Package `memory` is `List`, the three addresses of upstream's waitlist; it refuses a subscription in the `.invalid` domain, which takes no mail.
- Package `script` reads lines `name TEXT`, `email TEXT`, `note TEXT` and `submit` and plays them against the actor. After each event it prints `> EVENT` and the form, or `(ignored)` when the event changed nothing; when the event started a check or a submit, it also prints the form once the answer has come.
- `cmd/fk-form` binds `memory.List` (the `Subscribers` context) and is the only place that chooses it.

## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files.

## Laws

- `EditClearsThatFieldsError`: outside a submit, an edit sets its field to the field its text gives, an earlier error included, and touches nothing else; during a submit it changes nothing.
- `StaleCheckIsDropped`: an email edit opens a new entry, a name or note edit keeps it, and a check's answer when no check is under way changes nothing.
- `SubmitOnlyWhenAllValid`: over forms the user can reach, a submit starts exactly when the form is not sending, the name is valid or empty, and the email has passed its check.
- `EmailIsThePattern`: an address built of clean parts parses back; a space of any kind, a second `@`, or a domain with no inner dot is `Malformed`.
- `NameWhere` (derived from the where), `Subscribers.TakenIgnoresCase` (contract; runs for `memory.List`, and under the `Subscribers` context in `cmd/fk-form`).

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `subscribe`: upstream's flow: an ignored empty submit, a short and a long name, a malformed and an empty address, an address on the list (in another case), a new address, a submit and a second submit, then an empty name and a refused subscription.
- `invalid`: a short name blocks a checked address; six addresses the pattern refuses; a two-byte name, then a checked address, and the form is sent.
- `badscript`: an unknown line; the program says which line and exits 1.

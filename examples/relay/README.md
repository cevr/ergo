# relay

A chat relay's frame codecs. `relay` reads frames from its arguments, `note 3:annhi` or `join 2:bo`, and prints for each the line a room shows and the frame it forwards. A frame it cannot read ends it with status 1 and says why.

It shows generic capabilities and generic laws (L4):

- `chat.Codec[T]` is one operation type for every event kind. `chat.Read` needs `Codec[Note] | Codec[Join]`, two capabilities, and calls each with its type argument written: `Codec[Note].Decode(…)`. The binary binds the two to two providers in one `with context { chat.Codec[chat.Note]: text.NoteText, chat.Codec[chat.Join]: text.JoinText }`.
- `Codec[T]`'s law `RoundTrip(v T)` is generic. `ergo test` runs it in package `text` against each provider at the instance its operations fix: `NoteText` over generated `Note`s, `JoinText` over generated `Join`s.
- `chat.Event` is a union, and `Describe` and `Forward` match it exhaustively. `Malformed` is a typed failure that `main` turns into the exit status.

## Layout

| folder | what |
| --- | --- |
| `chat/` | The domain package: `Event` (`Note \| Join`), `Malformed`, the contract `Codec[T]` with its law, and `Read` and `Forward`. |
| `text/` | The plain-text codecs `NoteText` and `JoinText`. A Note is the byte length of its sender, a colon, the sender and the body, so either may hold a colon. |
| `cmd/relay/` | The binary: binds the codecs and the host, and prints each frame's event. |

## Cases

- `frames`: a note, a join and a note whose sender and body hold colons.
- `malformed`: a join whose seat is not a number stops the relay after the first frame.

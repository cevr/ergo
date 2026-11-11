package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// promptProbe restates std prompt's laws (stdlib.md, Std laws) as tests, and
// holds each prompt machine to a small reference model over generated keys
// (TyperMatchesModel, PickerMatchesModel, MarkerMatchesModel,
// AskerMatchesModel) and Ask to its contract on a term.Script
// (AskRunsOnATerminal).
const promptProbe = `
import io
import machines
import prompt
import strings
import term
import utf8

func keys(codes []int) []term.Key {
	let mut out = []term.Key{}
	for c in codes {
		out = out.Append(prompt.KeyOf(c))
	}
	out
}

func size(n int) int { if n < 0 { -(n % 5) } else { n % 5 } }

func optionsOf(n int) []string {
	let mut out = []string{}
	for i in 0..size(n) {
		out = out.Append("o" + strings.Repeat("i", i))
	}
	out
}

func ends(k term.Key) bool {
	match k {
	case term.Escape, term.Interrupt, term.EndOfInput:
		true
	case _:
		false
	}
}

// outcome is the answer of a run as text: "none" for no Done, "cancel"
// for a Done of None, the answer otherwise.
func outcome[S, O](o machines.Outcome[S, Option[O]], show func(v O) string) string {
	match o {
	case machines.Finished[S, Option[O]] as f:
		match f.Output {
		case Some[O] as a:
			show(a.Value)
		case None:
			"cancel"
		}
	case machines.Running[S, Option[O]]:
		"none"
	}
}

func intsText(xs []int) string {
	let mut out = ""
	for x in xs {
		out = out + "," + strings.Repeat("|", x)
	}
	out
}

func boolText(b bool) string { if b { "yes" } else { "no" } }

func same(s string) string { s }

func count(i int) string { strings.Repeat("|", i) }

// textModel is a text prompt's answer by a list of runes.
func textModel(fallback string, ks []term.Key) string {
	let mut runes = []string{}
	for k in ks {
		match k {
		case term.Char as c:
			runes = runes.Append(c.Text)
		case term.Backspace:
			if len(runes) > 0 {
				runes = runes[0:len(runes)-1]
			}
		case term.Return:
			let typed = strings.Join(runes, "")
			return if typed == "" { fallback } else { typed }
		case term.Escape, term.Interrupt, term.EndOfInput:
			return "cancel"
		case _:
			runes = runes
		}
	}
	"none"
}

// pickModel is a select's answer: the count of Downs and Tabs less the
// Ups, held to the options at each key.
func pickModel(n int, ks []term.Key) string {
	let mut at = 0
	for k in ks {
		match k {
		case term.Up:
			at = if at > 0 { at - 1 } else { 0 }
		case term.Down, term.Tab:
			at = if at < n - 1 { at + 1 } else { at }
		case term.Return:
			if n > 0 {
				return count(at)
			}
			return "rejected"
		case term.Escape, term.Interrupt, term.EndOfInput:
			return "cancel"
		case _:
			at = at
		}
	}
	"none"
}

// markModel is a multi-select's answer: each option marked by an odd
// count of spaces on it.
func markModel(n int, ks []term.Key) string {
	let mut at = 0
	let mut spaces = []int{}
	for _ in 0..n {
		spaces = spaces.Append(0)
	}
	for k in ks {
		match k {
		case term.Up:
			at = if at > 0 { at - 1 } else { 0 }
		case term.Down, term.Tab:
			at = if at < n - 1 { at + 1 } else { at }
		case term.Char as c:
			if c.Text == " " && n > 0 {
				spaces[at] = spaces[at] + 1
			}
		case term.Return:
			let mut out = []int{}
			for i, s in spaces {
				if s % 2 == 1 {
					out = out.Append(i)
				}
			}
			return intsText(out)
		case term.Escape, term.Interrupt, term.EndOfInput:
			return "cancel"
		case _:
			at = at
		}
	}
	"none"
}

// askModel is a confirm's answer: the first y, n or Return.
func askModel(fallback bool, ks []term.Key) string {
	for k in ks {
		match k {
		case term.Char as c:
			if strings.ToLower(c.Text) == "y" {
				return "yes"
			}
			if strings.ToLower(c.Text) == "n" {
				return "no"
			}
		case term.Return:
			return boolText(fallback)
		case term.Escape, term.Interrupt, term.EndOfInput:
			return "cancel"
		case _:
			continue
		}
	}
	"none"
}

// typing is the keys of codes with every key that ends a text prompt made
// a Backspace, then Return: a line typed and edited to its end.
func typing(codes []int) []term.Key {
	let mut out = []term.Key{}
	for c in codes {
		let k = prompt.KeyOf(c)
		match k {
		case term.Return, term.Escape, term.Interrupt, term.EndOfInput:
			out = out.Append(term.Backspace)
		case _:
			out = out.Append(k)
		}
	}
	out.Append(term.Return)
}

test TyperMatchesModel(fallback string, codes []int) {
	let ks = if len(codes) % 2 == 0 { keys(codes) } else { typing(codes) }
	let got = machines.Run(machines.Start(prompt.Typer{}, prompt.Text{Message: "m", Default: fallback, Masked: false}), ks) catch {
	case machines.Rejected:
		return
	}
	assert outcome(got, same) == textModel(fallback, ks)
}

test PickerMatchesModel(n int, codes []int) {
	let ks = keys(codes)
	let got = machines.Run(machines.Start(prompt.Picker{}, prompt.Choice{Message: "m", Options: optionsOf(n)}), ks) catch {
	case machines.Rejected:
		assert pickModel(size(n), ks) == "rejected"
		return
	}
	assert outcome(got, count) == pickModel(size(n), ks)
}

test MarkerMatchesModel(n int, codes []int) {
	let ks = keys(codes)
	let got = machines.Run(machines.Start(prompt.Marker{}, prompt.Choice{Message: "m", Options: optionsOf(n)}), ks) catch {
	case machines.Rejected:
		return
	}
	assert outcome(got, intsText) == markModel(size(n), ks)
}

test AskerMatchesModel(fallback bool, codes []int) {
	let ks = keys(codes)
	let got = machines.Run(machines.Start(prompt.Asker{}, prompt.Question{Message: "m", Default: fallback}), ks) catch {
	case machines.Rejected:
		return
	}
	assert outcome(got, boolText) == askModel(fallback, ks)
}

// detached is a terminal that is not one, as the host's is on a pipe.
type detached {}

func SetMode(_ detached, m term.Mode) fails term.NotTerminal { return term.NotTerminal }
func Restore(_ detached) {}
func ReadKey(_ detached) term.Key fails io.ReadFailed { term.EndOfInput }
func Size(_ detached) term.Window fails term.NotTerminal { return term.NotTerminal }

// AskRunsOnATerminal: Ask reads the script's keys in raw mode, restores
// it, draws each frame on io.Stderr and answers; a secret's frames show no
// character of it; with no terminal a prompt fails NotInteractive; a
// cancel key fails Cancelled.
// secretRun types secret into prompt.Secret on a Script and returns the
// answer, what was drawn, and the modes left set.
func secretRun(secret string) (string, string, int) {
	let mut typed = []term.Key{}
	for r in strings.Split(secret, "") {
		typed = typed.Append(term.Char{Text: r})
	}
	let s = term.NewScript(typed.Append(term.Return), term.Window{Cols: 80, Rows: 24})
	let r = io.NewRecorder()
	let got = with context { term.Terminal: s, io.Stderr: r } { prompt.Secret("pin") } catch {
	case _:
		"failed"
	}
	(got, r.Text(), len(s.Modes()))
}

test AskRunsOnATerminal(word string) {
	let r = io.NewRecorder()
	let (got, drawn, left) = secretRun(word)
	let (_, masked, _) = secretRun(strings.Repeat("x", len(strings.Split(word, ""))))
	assert got == word
	assert left == 0
	assert drawn == masked
	assert strings.Contains(drawn, "? pin " + strings.Repeat("*", utf8.RuneCount(word)))
	let picked = with context { term.Terminal: term.NewScript([]term.Key{term.Down, term.Down, term.Up, term.Return}, term.Window{Cols: 80, Rows: 24}), io.Stderr: r } { prompt.Select("which", []string{"a", "b", "c"}) } catch {
	case _:
		-1
	}
	assert picked == 1
	let none = with context { term.Terminal: detached, io.Stderr: r } { confirmText() } catch {
	case prompt.NotInteractive:
		"not interactive"
	case _:
		"other"
	}
	assert none == "not interactive"
	let cancelled = with context { term.Terminal: term.NewScript([]term.Key{term.Escape}, term.Window{Cols: 80, Rows: 24}), io.Stderr: r } { prompt.Line("name", "x") } catch {
	case prompt.Cancelled:
		"cancelled"
	case _:
		"other"
	}
	assert cancelled == "cancelled"
}

func confirmText() string fails prompt.NotInteractive | prompt.Cancelled | io.ReadFailed needs term.Terminal | io.Stderr {
	boolText(try prompt.Confirm("ok", true))
}

test SelectStaysInRange(n int, codes []int) {
	let options = optionsOf(n)
	let mut s = prompt.Picking{Ask: prompt.Choice{Message: "pick", Options: options}, At: 0}
	let mut ok = true
	for code in codes {
		match prompt.PickingStep(s, prompt.KeyOf(code)) {
		case Stay[prompt.Picking] as st:
			s = st.Next
		case Done[Option[int]] as d:
			match d.Output {
			case Some[int] as a:
				ok = ok && a.Value >= 0 && a.Value < len(options)
			case None:
				ok = ok
			}
			break
		case _:
			s = s
		}
		ok = ok && s.At >= 0 && (len(options) > 0 || s.At == 0) && (len(options) == 0 || s.At < len(options))
	}
	assert ok
}

test SecretFrameHidesText(secret string, message string) {
	let ask = prompt.Text{Message: message, Default: "", Masked: true}
	assert prompt.TypingFrame(prompt.Typing{Ask: ask, Typed: secret}) == prompt.TypingFrame(prompt.Typing{Ask: ask, Typed: strings.Repeat("x", utf8.RuneCount(secret))})
}

test SecretFramesHideTyping(codes []int) {
	let ask = prompt.Text{Message: "secret", Default: "", Masked: true}
	let mut s = prompt.Typing{Ask: ask, Typed: ""}
	for code in codes {
		match prompt.TypingStep(s, prompt.KeyOf(code)) {
		case Stay[prompt.Typing] as st:
			s = st.Next
			assert prompt.TypingFrame(s) == prompt.TypingFrame(prompt.Typing{Ask: ask, Typed: strings.Repeat("x", utf8.RuneCount(s.Typed))})
		case _:
			s = s
		}
	}
}

test MarkedAnswerIsSorted(n int, codes []int) {
	let options = optionsOf(n)
	let o = machines.Run(machines.Start(prompt.Marker{}, prompt.Choice{Message: "mark", Options: options}), keys(codes)) catch {
	case machines.Rejected:
		return
	}
	match o {
	case machines.Finished[prompt.Marking, Option[[]int]] as f:
		match f.Output {
		case Some[[]int] as a:
			let mut last = -1
			for i in a.Value {
				assert i > last && i < len(options)
				last = i
			}
		case None:
			return
		}
	case machines.Running[prompt.Marking, Option[[]int]]:
		return
	}
}

test ReturnAnswersTheDefault(yes bool, fallback string) {
	match prompt.AskingStep(prompt.Asking{Ask: prompt.Question{Message: "ok", Default: yes}}, term.Return) {
	case Done[Option[bool]] as d:
		assert d.Output == Some[bool]{Value: yes}
	case _:
		assert false
	}
	match prompt.TypingStep(prompt.Typing{Ask: prompt.Text{Message: "name", Default: fallback, Masked: false}, Typed: ""}, term.Return) {
	case Done[Option[string]] as d:
		assert d.Output == Some[string]{Value: fallback}
	case _:
		assert false
	}
}
`

// promptLawStatements are the ledger statements of prompt's laws.
var promptLawStatements = map[string]string{
	"prompt SelectStaysInRange":      `SelectStaysInRange(n int, codes []int) { let mut options = []string{} for i in 0..(if n < 0 { -(n % 5) } else { n % 5 }) { options = options.Append("o" + strings.Repeat("i", i)) } let mut s = startPicking(Choice{Message: "pick", Options: options}) for code in codes { match PickingStep(s, KeyOf(code)) { case Stay[Picking] as st: s = st.Next case Done[Option[int]] as d: match d.Output { case Some[int] as a: return a.Value >= 0 && a.Value < len(options) case None: return true } case _: s = s } if s.At < 0 || (len(options) == 0 && s.At != 0) || (len(options) > 0 && s.At >= len(options)) { return false } } true }`,
	"prompt SecretFrameHidesText":    `SecretFrameHidesText(secret string, message string) { let ask = Text{Message: message, Default: "", Masked: true} TypingFrame(Typing{Ask: ask, Typed: secret}) == TypingFrame(Typing{Ask: ask, Typed: strings.Repeat("x", utf8.RuneCount(secret))}) }`,
	"prompt SecretFramesHideTyping":  `SecretFramesHideTyping(codes []int) { let ask = Text{Message: "secret", Default: "", Masked: true} let mut s = startTyping(ask) for code in codes { match TypingStep(s, KeyOf(code)) { case Stay[Typing] as st: s = st.Next if TypingFrame(s) != TypingFrame(Typing{Ask: ask, Typed: strings.Repeat("x", utf8.RuneCount(s.Typed))}) { return false } case _: s = s } } true }`,
	"prompt MarkedAnswerIsSorted":    `MarkedAnswerIsSorted(n int, codes []int) { let mut options = []string{} for i in 0..(if n < 0 { -(n % 5) } else { n % 5 }) { options = options.Append("o" + strings.Repeat("i", i)) } let mut s = startMarking(Choice{Message: "mark", Options: options}) for code in codes { match MarkingStep(s, KeyOf(code)) { case Stay[Marking] as st: s = st.Next case Done[Option[[]int]] as d: match d.Output { case Some[[]int] as a: let mut last = -1 for i in a.Value { if i <= last || i >= len(options) { return false } last = i } return true case None: return true } case _: s = s } } true }`,
	"prompt ReturnAnswersTheDefault": `ReturnAnswersTheDefault(yes bool, fallback string) { let confirmed = match AskingStep(startAsking(Question{Message: "ok", Default: yes}), term.Return) { case Done[Option[bool]] as d: d.Output == Some[bool]{Value: yes} case _: false } let typed = match TypingStep(startTyping(Text{Message: "name", Default: fallback, Masked: false}), term.Return) { case Done[Option[string]] as d: d.Output == Some[string]{Value: fallback} case _: false } confirmed && typed }`,
}

// TestPromptLaws is CU3's acceptance: std prompt's laws (a select never
// leaves its range; a secret never shows in a frame; a multi-select's
// answer is sorted; Return answers the default), a seeded property of each
// prompt machine against a reference model of its own, and Ask on a
// scripted terminal (restored mode, masked frames, NotInteractive on a
// terminal that is not one, Cancelled on Escape). Its mutants are in the
// lower ledger (cu3-*). Replay a failing case with the printed seed:
// the probe is `ergo test --seed N`.
//
// It builds Go because the laws and properties run in the built test
// binary (ergo test), the only place a law runs.
func TestPromptLaws(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "promptlaws", promptProbe)}, true)
	if exes == nil {
		return
	}
	out, errout, code := runExeArgs(t, exes["promptlaws"], "--seed", "1")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errout)
	}
	lawledger.Run(t, lawsPassed(t, promptLawStatements, func(key string) string {
		return strings.TrimPrefix(key, "prompt ")
	}, promptProbe, out, "TyperMatchesModel", "PickerMatchesModel", "MarkerMatchesModel", "AskerMatchesModel", "AskRunsOnATerminal"))
}

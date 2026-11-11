package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdConfigLawsProbe restates the laws of std config (laws.ergo, the
// member laws of Config over Memory, and RevealIsTheValue) as Ergo
// property tests, with the package's private helpers (loadOne,
// loadSettings and the record types) written out again.
const stdConfigLawsProbe = `import config
import flag
import json
import strconv
import strings
import toml
import utf8

type one { N int }

func loadOne(spec config.Spec) Option[int] fails config.Unreadable | config.Malformed | WireWrongKind needs config.Config {
	let v = config.Load[one](spec) catch {
	case WireMissingField:
		return None
	case config.Unreadable as u:
		return u
	case config.Malformed as m:
		return m
	case WireWrongKind as w:
		return w
	}
	Some{Value: v.N}
}

test ConfigHigherLayerWins(f Option[int], e Option[int], file Option[int], d Option[int]) {
	let set = flag.Set{Name: "t", Usage: "", Flags: []flag.Flag{flag.OptionalInt("n", "")}, Args: []flag.ArgSpec{}}
	let mut args = []string{}
	let mut env = Map[string, string]{}
	let mut files = Map[string, string]{}
	let mut defaults = config.NoDefaults()
	let mut want Option[int] = None
	match d {
	case Some as x:
		defaults = config.DefaultsOf(one{N: x.Value})
		want = x
	case None:
	}
	match file {
	case Some as x:
		files = Map[string, string]{"c.json": "{\"n\": " + strconv.Itoa(x.Value) + "}"}
		want = x
	case None:
	}
	match e {
	case Some as x:
		env = Map[string, string]{"T_N": strconv.Itoa(x.Value), "TN": "0", "T": "0"}
		want = x
	case None:
	}
	match f {
	case Some as x:
		args = []string{"--n=" + strconv.Itoa(x.Value)}
		want = x
	case None:
	}
	let v = flag.Parse(set, args) catch {
	case _:
		assert false
		return
	}
	let spec = config.Spec{Prefix: "T", Flags: config.FromFlags(v), Files: []config.File{config.File{Path: "c.json", Format: config.JSON}, config.File{Path: "none.toml", Format: config.TOML}}, Defaults: defaults}
	let got = with context { config.Config: config.NewMemory(env, files) } {
		loadOne(spec)
	} catch {
	case _:
		assert false
		return
	}
	assert got == want
}

type server { Host string, Debug bool }
type quiet {}
type loud { Level int }
type volume = quiet | loud
type settings { Name string, Port int, Ratio float64, Tags []string, Server server, Volume volume }

func loadSettings(path string, format config.Format) Option[settings] needs config.Config {
	let spec = config.Spec{Prefix: "", Flags: config.NoFlags(), Files: []config.File{config.File{Path: path, Format: format}}, Defaults: config.NoDefaults()}
	let v = config.Load[settings](spec) catch {
	case _:
		return None
	}
	Some{Value: v}
}

test ConfigRoundTripsThroughFiles(v settings) {
	let mut texts = utf8.ValidString(v.Name) && utf8.ValidString(v.Server.Host)
	for t in v.Tags {
		texts = texts && utf8.ValidString(t)
	}
	if !texts {
		return
	}
	let asJSON = json.Encode(json.Codec[settings]{MaxBytes: 0, MaxDepth: 0}, v)
	let asTOML = toml.Encode(v) catch {
	case toml.Unrepresentable:
		assert false
		return
	}
	let files = Map[string, string]{"c.json": asJSON, "c.toml": asTOML}
	with context { config.Config: config.NewMemory(Map[string, string]{}, files) } {
		assert loadSettings("c.json", config.JSON) == Some{Value: v}
		assert loadSettings("c.toml", config.TOML) == Some{Value: v}
	}
}

test ConfigNamesInvert(picks []int) {
	let words = []string{"port", "apiToken", "httpStatus", "v2Path", "x", "userID", "logLevel"}
	let mut parts = []string{}
	for p in picks {
		parts = parts.Append(words[if p < 0 { -(p % len(words)) } else { p % len(words) }])
	}
	if len(parts) == 0 {
		return
	}
	let key = strings.Join(parts, ".")
	assert config.FlagKey(config.FlagName(key)) == key
	assert config.EnvKey("P", config.EnvName("P", key)) == key
}

test ConfigRevealIsTheValue(v string, n int) {
	assert config.NewRedacted(v).Reveal() == v && config.NewRedacted(n).Reveal() == n
}

test ConfigConfigEnvironAgreesWithLookup(m config.Memory, name string) {
	let vars = m.Environ(name)
	let mut sorted = true
	for i, v in vars {
		assert strings.HasPrefix(v.Name, name) && m.Lookup(v.Name) == (Some{Value: v.Text})
		if i > 0 && strings.Compare(vars[i-1].Name, v.Name) >= 0 {
			sorted = false
		}
	}
	let listed = len(vars) > 0 && vars[0].Name == name
	match m.Lookup(name) {
	case Some:
		assert sorted && listed
	case None:
		assert sorted && !listed
	}
}

test ConfigConfigReadIsRepeatable(m config.Memory, path string) {
	let first = m.ReadFile(path) catch {
	case config.Unreadable as u:
		let _ = m.ReadFile(path) catch {
		case config.Unreadable as v:
			assert u == v
			return
		}
		assert false
		return
	}
	let second = m.ReadFile(path) catch {
	case config.Unreadable:
		assert false
		return
	}
	assert first == second
}
`

// stdConfigLawStatements is the statement of each law the probe restates,
// by ledger key; lawledger.Run holds it equal to the ledger.
var stdConfigLawStatements = map[string]string{
	"config HigherLayerWins":                `HigherLayerWins(f Option[int], e Option[int], file Option[int], d Option[int]) { let set = flag.Set{Name: "t", Usage: "", Flags: []flag.Flag{flag.OptionalInt("n", "")}, Args: []flag.ArgSpec{}} let mut args = []string{} let mut env = Map[string, string]{} let mut files = Map[string, string]{} let mut defaults = NoDefaults() let mut want Option[int] = None match d { case Some as x: defaults = DefaultsOf(one{N: x.Value}) want = x case None: } match file { case Some as x: files = Map[string, string]{"c.json": "{\"n\": " + strconv.Itoa(x.Value) + "}"} want = x case None: } match e { case Some as x: env = Map[string, string]{"T_N": strconv.Itoa(x.Value), "TN": "0", "T": "0"} want = x case None: } match f { case Some as x: args = []string{"--n=" + strconv.Itoa(x.Value)} want = x case None: } let v = flag.Parse(set, args) catch { case _: return false } let spec = Spec{Prefix: "T", Flags: FromFlags(v), Files: []File{File{Path: "c.json", Format: JSON}, File{Path: "none.toml", Format: TOML}}, Defaults: defaults} let got = with context { Config: NewMemory(env, files) } { loadOne(spec) } catch { case _: return false } got == want }`,
	"config RoundTripsThroughFiles":         `RoundTripsThroughFiles(v settings) { let mut texts = utf8.ValidString(v.Name) && utf8.ValidString(v.Server.Host) for t in v.Tags { texts = texts && utf8.ValidString(t) } if !texts { return true } let asJSON = json.Encode(json.Codec[settings]{MaxBytes: 0, MaxDepth: 0}, v) let asTOML = toml.Encode(v) catch { case toml.Unrepresentable: return false } let files = Map[string, string]{"c.json": asJSON, "c.toml": asTOML} with context { Config: NewMemory(Map[string, string]{}, files) } { loadSettings("c.json", JSON) == Some{Value: v} && loadSettings("c.toml", TOML) == Some{Value: v} } }`,
	"config NamesInvert":                    `NamesInvert(picks []int) { let words = []string{"port", "apiToken", "httpStatus", "v2Path", "x", "userID", "logLevel"} let mut parts = []string{} for p in picks { parts = parts.Append(words[if p < 0 { -(p % len(words)) } else { p % len(words) }]) } if len(parts) == 0 { return true } let key = strings.Join(parts, ".") FlagKey(FlagName(key)) == key && EnvKey("P", EnvName("P", key)) == key }`,
	"config RevealIsTheValue":               `RevealIsTheValue(v string, n int) { NewRedacted(v).Reveal() == v && NewRedacted(n).Reveal() == n }`,
	"config Config.EnvironAgreesWithLookup": `EnvironAgreesWithLookup(name string) { let vars = Environ(name) let mut sorted = true for i, v in vars { if !strings.HasPrefix(v.Name, name) || Lookup(v.Name) != (Some{Value: v.Text}) { return false } if i > 0 && strings.Compare(vars[i-1].Name, v.Name) >= 0 { sorted = false } } let listed = len(vars) > 0 && vars[0].Name == name match Lookup(name) { case Some: sorted && listed case None: sorted && !listed } }`,
	"config Config.ReadIsRepeatable":        `ReadIsRepeatable(path string) { let first = ReadFile(path) catch { case Unreadable as u: let _ = ReadFile(path) catch { case Unreadable as v: return u == v } return false } let second = ReadFile(path) catch { case Unreadable: return false } first == second }`,
}

// TestStdConfigLawsRun runs the config probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed. The member laws of Config run over a generated Memory; the Host
// provider reads the process's own environment and disk, which a seeded
// case cannot set.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdConfigLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdconfig", stdConfigLawsProbe, stdConfigLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdJSONSchemaLawsProbe restates the laws of std jsonschema (wf10 JS0)
// as Ergo tests over its exported API: Parse, Conforms and Invalid, with
// the package's helpers restated.
const stdJSONSchemaLawsProbe = `import json
import jsonschema
import strings

// parsed is the Schema text spells, or the schema no value fits.
func parsed(text string) jsonschema.Schema {
	jsonschema.Parse(text) catch {
	case jsonschema.Invalid:
		nothing()
	}
}

// nothing is the schema no value fits.
func nothing() jsonschema.Schema {
	jsonschema.Parse("false") catch {
	case jsonschema.Invalid:
		Unreachable("false is a schema")
	}
}

// value is the JSON value text spells, or null.
func value(text string) json.Value {
	json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case _:
		json.Null
	}
}

// refusal is the path Parse refuses text at, or None when it is a
// Schema.
func refusal(text string) Option[string] {
	let _ = jsonschema.Parse(text) catch {
	case jsonschema.Invalid as i:
		return Some[string]{Value: i.Path}
	}
	None
}

// quoted is s as a JSON string.
func quoted(s string) string { json.Encode(json.Codec[string]{MaxBytes: 0, MaxDepth: 0}, s) }

// pointer is key as a JSON pointer's token.
func pointer(key string) string { strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1") }

func lawEncodedIsTheSchema(key string, n int) bool {
	let s = parsed("{\"type\":\"object\",\"properties\":{" + quoted(key) + ":{\"type\":\"integer\",\"const\":\(n)}},\"required\":[" + quoted(key) + "]}")
	let e = jsonschema.Encode(s)
	let back = jsonschema.Parse(jsonschema.TextOf(e)) catch {
	case jsonschema.Invalid:
		return false
	}
	jsonschema.TextOf(e) == jsonschema.Text(s) && jsonschema.SchemaOf(e) == s && back == s
}

test JsonschemaEncodedIsTheSchema(key string, n int) { assert lawEncodedIsTheSchema(key, n) }

func lawNewEncodedRequiresItsKeys(keys []string, s string) bool {
	let e = jsonschema.NewEncoded(keys)
	let back = jsonschema.Parse(jsonschema.TextOf(e)) catch {
	case jsonschema.Invalid:
		return false
	}
	let mut members = []json.Member{}
	for k in keys {
		members = members.Append(json.Member{Key: k, Value: json.Str{Value: s}})
	}
	let mut fits = back == jsonschema.SchemaOf(e) && jsonschema.Conforms(jsonschema.SchemaOf(e), json.Object{Members: members})
	if len(keys) > 0 {
		let mut rest = []json.Member{}
		for m in members {
			if m.Key != keys[0] {
				rest = rest.Append(m)
			}
		}
		fits = fits && !jsonschema.Conforms(jsonschema.SchemaOf(e), json.Object{Members: rest})
	}
	fits
}

test JsonschemaNewEncodedRequiresItsKeys(keys []string, s string) { assert lawNewEncodedRequiresItsKeys(keys, s) }

func lawRequiredKeyIsRequired(key string, keys []string) bool {
	let schema = parsed("{\"type\":\"object\",\"required\":[" + quoted(key) + "]}")
	let mut members = []json.Member{}
	let mut has = false
	for k in keys {
		members = members.Append(json.Member{Key: k, Value: json.Null})
		has = has || k == key
	}
	jsonschema.Conforms(schema, json.Object{Members: members}) == has
}

test JsonschemaRequiredKeyIsRequired(key string, keys []string) { assert lawRequiredKeyIsRequired(key, keys) }

func lawPropertiesAreChecked(n int, s string) bool {
	let schema = parsed("{\"type\":\"object\",\"properties\":{\"n\":{\"type\":\"integer\"}},\"additionalProperties\":{\"type\":\"string\"}}")
	let good = json.Object{Members: []json.Member{json.Member{Key: "n", Value: json.IntNumber(int64(n))}, json.Member{Key: "m", Value: json.Str{Value: s}}}}
	let bad = json.Object{Members: []json.Member{json.Member{Key: "n", Value: json.Str{Value: s}}}}
	let extra = json.Object{Members: []json.Member{json.Member{Key: "m", Value: json.IntNumber(int64(n))}}}
	jsonschema.Conforms(schema, good) && !jsonschema.Conforms(schema, bad) && !jsonschema.Conforms(schema, extra)
}

test JsonschemaPropertiesAreChecked(n int, s string) { assert lawPropertiesAreChecked(n, s) }

func lawIntegerHasNoFraction(n int) bool {
	let integer = parsed("{\"type\":\"integer\"}")
	let num = parsed("{\"type\":\"number\"}")
	let frac = value("\(n).5")
	let whole = value("\(n).0")
	let exp = value("\(n)e1")
	let small = value("\(n).25e1")
	let moved = value("\(n).20e1")
	jsonschema.Conforms(integer, json.IntNumber(int64(n))) && !jsonschema.Conforms(integer, frac) && jsonschema.Conforms(integer, whole) && jsonschema.Conforms(integer, exp) && !jsonschema.Conforms(integer, small) && jsonschema.Conforms(integer, moved) && jsonschema.Conforms(num, frac) && jsonschema.Conforms(num, small)
}

test JsonschemaIntegerHasNoFraction(n int) { assert lawIntegerHasNoFraction(n) }

func lawAnyOfIsOneOfItsSchemas(s string) bool {
	let schema = parsed("{\"anyOf\":[{\"const\":\"A\"},{\"const\":\"B\"}]}")
	jsonschema.Conforms(schema, json.Str{Value: s}) == (s == "A" || s == "B")
}

test JsonschemaAnyOfIsOneOfItsSchemas(s string) { assert lawAnyOfIsOneOfItsSchemas(s) }

func lawConstIsOneValue(s string, t string, n int) bool {
	let schema = parsed("{\"const\":{\"s\":" + quoted(s) + ",\"n\":\(n)}}")
	let other = json.Object{Members: []json.Member{json.Member{Key: "s", Value: json.Str{Value: t}}, json.Member{Key: "n", Value: json.IntNumber(int64(n))}}}
	jsonschema.Conforms(schema, value("{\"s\":"+quoted(s)+",\"n\":\(n)}")) && jsonschema.Conforms(schema, other) == (s == t) && !jsonschema.Conforms(schema, json.Str{Value: s})
}

test JsonschemaConstIsOneValue(s string, t string, n int) { assert lawConstIsOneValue(s, t, n) }

func lawPrefixItemsFixTheLength(xs []string) bool {
	let schema = parsed("{\"type\":\"array\",\"prefixItems\":[{\"type\":\"string\"},{\"type\":\"integer\"}]}")
	let mut items = []json.Value{}
	for x in xs {
		items = items.Append(json.Str{Value: x})
	}
	let mixed = json.Array{Items: []json.Value{json.Str{Value: "a"}, json.IntNumber(1)}}
	let swapped = json.Array{Items: []json.Value{json.IntNumber(1), json.Str{Value: "a"}}}
	let longer = json.Array{Items: mixed.Items.Append(json.Str{Value: "c"})}
	!jsonschema.Conforms(schema, json.Array{Items: items}) && jsonschema.Conforms(schema, mixed) && !jsonschema.Conforms(schema, swapped) && !jsonschema.Conforms(schema, longer)
}

test JsonschemaPrefixItemsFixTheLength(xs []string) { assert lawPrefixItemsFixTheLength(xs) }

func lawItemsAreEachChecked(ns []int) bool {
	let schema = parsed("{\"type\":\"array\",\"items\":{\"type\":\"integer\"},\"minItems\":1,\"maxItems\":3}")
	let mut items = []json.Value{}
	for n in ns {
		items = items.Append(json.IntNumber(int64(n)))
	}
	jsonschema.Conforms(schema, json.Array{Items: items}) == (len(ns) >= 1 && len(ns) <= 3) && !jsonschema.Conforms(schema, json.Array{Items: items.Append(json.Str{Value: "x"})})
}

test JsonschemaItemsAreEachChecked(ns []int) { assert lawItemsAreEachChecked(ns) }

func lawRefsAreLocal(name string, host string, s string) bool {
	let defs = "{\"$defs\":{" + quoted(name) + ":{\"type\":\"string\"}},\"$ref\":" + quoted("#/$defs/"+pointer(name)) + "}"
	let local = jsonschema.Parse(defs) catch {
	case jsonschema.Invalid:
		return false
	}
	let remote = refusal("{\"$ref\":" + quoted("https://"+host+"/schema.json") + "}")
	jsonschema.Conforms(local, json.Str{Value: s}) && !jsonschema.Conforms(local, json.IntNumber(1)) && remote == Some[string]{Value: "/$ref"}
}

test JsonschemaRefsAreLocal(name string, host string, s string) { assert lawRefsAreLocal(name, host, s) }

func lawRefsRecurse(n int) bool {
	let schema = parsed("{\"$defs\":{\"list\":{\"anyOf\":[{\"type\":\"null\"},{\"type\":\"object\",\"properties\":{\"next\":{\"$ref\":\"#/$defs/list\"}},\"required\":[\"next\"]}]}},\"$ref\":\"#/$defs/list\"}")
	let mut good = "null"
	let mut bad = "1"
	for _ in 0..(n%3+3)%3 {
		good = "{\"next\":" + good + "}"
		bad = "{\"next\":" + bad + "}"
	}
	jsonschema.Conforms(schema, value(good)) && !jsonschema.Conforms(schema, value(bad)) && !jsonschema.Conforms(parsed("{\"$defs\":{\"a\":{\"$ref\":\"#\"}},\"$ref\":\"#/$defs/a\"}"), value(good))
}

test JsonschemaRefsRecurse(n int) { assert lawRefsRecurse(n) }

func lawKeywordsAreChecked(key string) bool {
	let at = "/properties/" + pointer(key)
	let bad = []string{"{\"type\":\"text\"}", "{\"required\":\"a\"}", "{\"required\":[1]}", "{\"items\":1}", "{\"anyOf\":[]}", "{\"minItems\":-1}", "{\"$ref\":1}"}
	let mut all = true
	for b in bad {
		let refused = match refusal("{\"properties\":{" + quoted(key) + ":" + b + "}}") {
		case Some[string] as path:
			strings.HasPrefix(path.Value, at+"/")
		case None:
			false
		}
		all = all && refused
	}
	all
}

test JsonschemaKeywordsAreChecked(key string) { assert lawKeywordsAreChecked(key) }
`

// stdJSONSchemaLawStatements is the statement of each jsonschema law, by
// ledger key; lawledger.Run holds it equal to the ledger.
var stdJSONSchemaLawStatements = map[string]string{
	"jsonschema EncodedIsTheSchema":        `EncodedIsTheSchema(key string, n int) { let s = parsed("{\"type\":\"object\",\"properties\":{" + quoted(key) + ":{\"type\":\"integer\",\"const\":\(n)}},\"required\":[" + quoted(key) + "]}") let e = Encode(s) let back = Parse(TextOf(e)) catch { case Invalid: return false } TextOf(e) == Text(s) && SchemaOf(e) == s && back == s }`,
	"jsonschema NewEncodedRequiresItsKeys": `NewEncodedRequiresItsKeys(keys []string, s string) { let e = NewEncoded(keys) let back = Parse(TextOf(e)) catch { case Invalid: return false } let mut members = []json.Member{} for k in keys { members = members.Append(json.Member{Key: k, Value: json.Str{Value: s}}) } let mut fits = back == SchemaOf(e) && Conforms(SchemaOf(e), json.Object{Members: members}) if len(keys) > 0 { let mut rest = []json.Member{} for m in members { if m.Key != keys[0] { rest = rest.Append(m) } } fits = fits && !Conforms(SchemaOf(e), json.Object{Members: rest}) } fits }`,
	"jsonschema RequiredKeyIsRequired":     `RequiredKeyIsRequired(key string, keys []string) { let schema = parsed("{\"type\":\"object\",\"required\":[" + quoted(key) + "]}") let mut members = []json.Member{} let mut has = false for k in keys { members = members.Append(json.Member{Key: k, Value: json.Null}) has = has || k == key } Conforms(schema, json.Object{Members: members}) == has }`,
	"jsonschema PropertiesAreChecked":      `PropertiesAreChecked(n int, s string) { let schema = parsed("{\"type\":\"object\",\"properties\":{\"n\":{\"type\":\"integer\"}},\"additionalProperties\":{\"type\":\"string\"}}") let good = json.Object{Members: []json.Member{json.Member{Key: "n", Value: json.IntNumber(int64(n))}, json.Member{Key: "m", Value: json.Str{Value: s}}}} let bad = json.Object{Members: []json.Member{json.Member{Key: "n", Value: json.Str{Value: s}}}} let extra = json.Object{Members: []json.Member{json.Member{Key: "m", Value: json.IntNumber(int64(n))}}} Conforms(schema, good) && !Conforms(schema, bad) && !Conforms(schema, extra) }`,
	"jsonschema IntegerHasNoFraction":      `IntegerHasNoFraction(n int) { let integer = parsed("{\"type\":\"integer\"}") let num = parsed("{\"type\":\"number\"}") let frac = value("\(n).5") let whole = value("\(n).0") let exp = value("\(n)e1") let small = value("\(n).25e1") let moved = value("\(n).20e1") Conforms(integer, json.IntNumber(int64(n))) && !Conforms(integer, frac) && Conforms(integer, whole) && Conforms(integer, exp) && !Conforms(integer, small) && Conforms(integer, moved) && Conforms(num, frac) && Conforms(num, small) }`,
	"jsonschema AnyOfIsOneOfItsSchemas":    `AnyOfIsOneOfItsSchemas(s string) { let schema = parsed("{\"anyOf\":[{\"const\":\"A\"},{\"const\":\"B\"}]}") Conforms(schema, json.Str{Value: s}) == (s == "A" || s == "B") }`,
	"jsonschema ConstIsOneValue":           `ConstIsOneValue(s string, t string, n int) { let schema = parsed("{\"const\":{\"s\":" + quoted(s) + ",\"n\":\(n)}}") let other = json.Object{Members: []json.Member{json.Member{Key: "s", Value: json.Str{Value: t}}, json.Member{Key: "n", Value: json.IntNumber(int64(n))}}} Conforms(schema, value("{\"s\":"+quoted(s)+",\"n\":\(n)}")) && Conforms(schema, other) == (s == t) && !Conforms(schema, json.Str{Value: s}) }`,
	"jsonschema PrefixItemsFixTheLength":   `PrefixItemsFixTheLength(xs []string) { let schema = parsed("{\"type\":\"array\",\"prefixItems\":[{\"type\":\"string\"},{\"type\":\"integer\"}]}") let mut items = []json.Value{} for x in xs { items = items.Append(json.Str{Value: x}) } let mixed = json.Array{Items: []json.Value{json.Str{Value: "a"}, json.IntNumber(1)}} let swapped = json.Array{Items: []json.Value{json.IntNumber(1), json.Str{Value: "a"}}} let longer = json.Array{Items: mixed.Items.Append(json.Str{Value: "c"})} !Conforms(schema, json.Array{Items: items}) && Conforms(schema, mixed) && !Conforms(schema, swapped) && !Conforms(schema, longer) }`,
	"jsonschema ItemsAreEachChecked":       `ItemsAreEachChecked(ns []int) { let schema = parsed("{\"type\":\"array\",\"items\":{\"type\":\"integer\"},\"minItems\":1,\"maxItems\":3}") let mut items = []json.Value{} for n in ns { items = items.Append(json.IntNumber(int64(n))) } Conforms(schema, json.Array{Items: items}) == (len(ns) >= 1 && len(ns) <= 3) && !Conforms(schema, json.Array{Items: items.Append(json.Str{Value: "x"})}) }`,
	"jsonschema RefsAreLocal":              `RefsAreLocal(name string, host string, s string) { let defs = "{\"$defs\":{" + quoted(name) + ":{\"type\":\"string\"}},\"$ref\":" + quoted("#/$defs/"+pointer(name)) + "}" let local = Parse(defs) catch { case Invalid: return false } let remote = refusal("{\"$ref\":" + quoted("https://"+host+"/schema.json") + "}") Conforms(local, json.Str{Value: s}) && !Conforms(local, json.IntNumber(1)) && remote == Some[string]{Value: "/$ref"} }`,
	"jsonschema RefsRecurse":               `RefsRecurse(n int) { let schema = parsed("{\"$defs\":{\"list\":{\"anyOf\":[{\"type\":\"null\"},{\"type\":\"object\",\"properties\":{\"next\":{\"$ref\":\"#/$defs/list\"}},\"required\":[\"next\"]}]}},\"$ref\":\"#/$defs/list\"}") let mut good = "null" let mut bad = "1" for _ in 0..(n%3+3)%3 { good = "{\"next\":" + good + "}" bad = "{\"next\":" + bad + "}" } Conforms(schema, value(good)) && !Conforms(schema, value(bad)) && !Conforms(parsed("{\"$defs\":{\"a\":{\"$ref\":\"#\"}},\"$ref\":\"#/$defs/a\"}"), value(good)) }`,
	"jsonschema KeywordsAreChecked":        `KeywordsAreChecked(key string) { let at = "/properties/" + pointer(key) let bad = []string{"{\"type\":\"text\"}", "{\"required\":\"a\"}", "{\"required\":[1]}", "{\"items\":1}", "{\"anyOf\":[]}", "{\"minItems\":-1}", "{\"$ref\":1}"} let mut all = true for b in bad { let refused = match refusal("{\"properties\":{" + quoted(key) + ":" + b + "}}") { case Some[string] as path: strings.HasPrefix(path.Value, at+"/") case None: false } all = all && refused } all }`,
}

// TestStdJSONSchemaLawsRun runs std jsonschema's laws as the probe's tests
// with --seed 1; lawledger.Run holds that the probe restates exactly the
// ledger's jsonschema laws, each with the row's statement, and that each
// passed. The jsonschema-* mutants of the std mutant ledger name it. It
// builds Go because each law is an Ergo test the built probe runs, as
// TestStdCronLawsRun's are.
func TestStdJSONSchemaLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdjsonschema", stdJSONSchemaLawsProbe, stdJSONSchemaLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

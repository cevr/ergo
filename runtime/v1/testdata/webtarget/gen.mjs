// Records the WHATWG request targets that TestHTTPFetchSendsTheWebTarget
// checks on the wire (S-target). Each row is a generated URL (or a base and
// a redirect Location) and the target the reference URL parser gives: the
// serialized path and query, without the fragment. The rows are recorded
// once, from a reference implementation, and checked in, so the Go test
// needs no JavaScript and no network:
//
//	bun runtime/v1/testdata/webtarget/gen.mjs > runtime/v1/testdata/webtarget/targets.jsonl
//
// node gives the same text (ada); a difference between them is a question
// for the standard, not a row to keep. The host is h.test; the test puts
// its loopback origin in its place (the target does not depend on it).

let state = 20261110
function next() {
  // mulberry32
  state = (state + 0x6d2b79f5) | 0
  let t = Math.imul(state ^ (state >>> 15), 1 | state)
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296
}
const pick = xs => xs[Math.floor(next() * xs.length)]
const many = (tokens, max) => Array.from({ length: Math.floor(next() * (max + 1)) }, () => pick(tokens)).join("")

const pathTokens = ["a", "b", "c.txt", "..", ".", "%2e", "%2E", "%2e%2E", ".%2e", "%2e.", "%zz", "%2F", "%41", "%", "|", "^", "`", "{", "}", "\"", "<", ">", " ", "'", "\\", "/", "/", "/", "é", "€", "😀", "\x01", "\x7f", "\t", "\n", "[", "]", "@", ":", ";", "=", "&", "+", "!", "$", "~", "\ud800"]
const queryTokens = [...pathTokens, "?", "=", "&"]
const fragmentTokens = ["x", "#", "?", " ", "|"]
const rows = []

function target(href, base) {
  const u = base === undefined ? new URL(href) : new URL(href, base)
  u.hash = ""
  return u.href.slice(u.origin.length)
}

for (let i = 0; i < 400; i++) {
  const lead = pick(["", "", "", " ", "\x01 ", "\t"])
  const trail = pick(["", "", "", " ", " \x01", "\n"])
  const scheme = pick(["http", "http", "HTTP", "HtTp"])
  const sep = pick(["//", "//", "//", "\\\\", "/\\"])
  let rest = pick(["/", "/", "/", "\\", ""]) + many(pathTokens, 8)
  if (next() < 0.4) rest += "?" + many(queryTokens, 6)
  if (next() < 0.2) rest += "#" + many(fragmentTokens, 3)
  if (rest !== "" && !"/\\?#".includes(rest[0])) rest = "/" + rest
  const url = lead + scheme + ":" + sep + "h.test" + rest + trail
  rows.push({ lead, scheme, sep, rest, trail, target: target(url) })
}

const locationTokens = ["a", "b", "..", ".", "%2e", "%zz", "%2F", "|", "^", "`", "{", "}", "'", "\\", "/", "é", "[", "]", "@", ";", "=", "&", "+", "~"]
for (let i = 0; i < 200; i++) {
  const base = "/" + many(["a", "b", "/", "%zz", "|", "^"], 5) + (next() < 0.3 ? "?q" : "")
  const form = pick(["path", "path", "absolute", "absolute", "query", "fragment", "same-scheme", "backslash"])
  let location = many(locationTokens, 6)
  // One slash: two would name a new authority.
  if (form === "absolute") location = "/" + location.replace(/^[\\/]+/, "")
  if (form === "backslash") location = "\\" + location.replace(/^[\\/]+/, "")
  if (form === "query") location = "?" + location
  if (form === "fragment") location = "#" + location
  if (form === "same-scheme") location = "http:" + location.replace(/^[\\/]+/, "")
  if (form === "path") location = location.replace(/^[\\/?#]+/, "") || "x"
  location = location.trim()
  if (location === "") location = "x"
  rows.push({ base, baseTarget: target("http://h.test" + base), location, target: target(location, "http://h.test" + base) })
}

for (const row of rows) console.log(JSON.stringify(row))

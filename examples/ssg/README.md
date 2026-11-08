# ssg

A static site generator. It reads the pages under `SITE/content` (Markdown-like text with front matter) and the templates under `SITE/templates`, renders each page through its layout, and writes the pages under `-out` (default `public`), or prints them with `-print`. It is an original program: its goldens were written from its own output and checked by hand.

Tag: `default`.

## Layout

The program is a module of packages (`ergo.mod`: `module ssg`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `site/` | The domain. `template.ergo` (the template language: `Template`, the `Node` union and `Parse`), `render.ergo` (`Value`, `Scope`, `Library` and `Render`), `escape.ergo` (`Escape`, `Unescape`, `Inert`), `page.ergo` (`Page`, front matter and `Markup`), `build.ergo` (`Build`, `Output`), `source.ergo` (`Source`, the contract it needs, with its laws), `laws.ergo` and `tests.ergo`. Only `Build` needs a capability: `Source`. |
| `memory/` | `Source`, the in-memory provider of `site.Source`, and `Disk`, an in-memory `fs.Files` and `fs.Writes` for tests. |
| `files/` | `Read`, which walks a site folder into a `memory.Source` through `fs.Files`, and `Write`, which writes the pages through `fs.Writes`. |
| `cmd/ssg/` | The binary. `main.ergo` reads the command line with std `flag`, binds the host's file system, standard output and arguments, and is the only place that chooses a provider. |
| `testdata/site/` | A small site: four pages (one a draft) and four templates. |

## The template language

| tag | meaning |
| --- | --- |
| `{{ a.b }}` | the text at `a.b`, escaped |
| `{{ raw a.b }}` | the text at `a.b`, as it is (a page's `body` is HTML) |
| `{{ for x in a.b }} … {{ end }}` | the body once for each item of the list `a.b`, with `x` bound to it |
| `{{ if a.b }} … {{ end }}` | the body when `a.b` is text that is not empty, a list that is not empty, or a record; a missing name is false |
| `{{ include name }}` | the template `name`, over the same variables |

A page sees `title`, `url`, `body`, `meta` (its front matter, a record) and `pages` (every page that is not a draft, as a record of `title` and `url`, in path order). Its layout is the template its front matter names as `layout`, or `page`.

## What it shows

- **Typed failures with their position.** `Parse` fails `Unclosed | Stray | BadTag`, `Render` fails `Unbound | NotAList | NotText | MissingTemplate | IncludeCycle`, each with the template and the line of its tag. `run` in `cmd/ssg` catches all eleven failures of the read and the build by name, with no `case _`, and prints one line per failure, such as `ssg: templates/nav.html:1: unclosed for`.
- **Unions and exhaustive `match`.** The template is a `Node` union (`Lit | Var | Each | When | Include`), a variable's value a `Value` union (`Text | Record | List`), and the scope a persistent `Scope` union (`Top | Bound`): a loop binds its name in a new `Bound`, so nothing is changed in place and no name leaks out of a loop. Each `match` covers every variant.
- **A contract with laws.** `site.Source` is an operation type with three member laws: `PagesAreSorted`, `ListedPagesRead` and `PageIsRepeatable`. `memory/tests.ergo` runs their checks against a `memory.Source`.
- **Laws over the domain.** `site/laws.ergo` states `EscapingIsTotal` (every text has an escape with no markup, and the escape reads back), `VariablesAreEscaped` (`{{ v }}` is `Escape(v)`, `{{ raw v }}` is `v`) and `RenderingIsDeterministic` (one template over one scope renders one text every time: a record's fields are a Map, whose order is not fixed, so no rendering may read it in order).
- **Providers chosen once.** `main` binds the host's file system; the tests bind a `memory.Disk` and an `io.Recorder`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
cp -R examples/ssg/testdata/site /tmp/site
./bin/ergo build examples/ssg/cmd/ssg -o /tmp/ssg
/tmp/ssg -print /tmp/site
./bin/ergo test examples/ssg/site
go run ./scripts/examples ssg
```

## Cases

`print` (every page on standard output), `write` (the pages under `public/`), `broken` (a template with a `for` and no `end`: status 1 and the file and line on standard error) and `usage` (no site folder: status 2 and the help).

## Limits found

Each limit is in the lane's gap file (`gaps-ex-g.md`, or the gap of the lane that found it first).

- **A provider cannot use a capability** (G-ex-g-2). A `site.Source` that reads each page from the disk when the build asks for it is refused (S106, rows.md Context 2), so `files.Read` reads the whole site into a `memory.Source` before the build.
- **A catch does not reach a failing argument, and the diagnostics contradict each other** (G-ex-g-1). `truthy(lookup(…)) catch { … }` is both "cannot fail" and "not handled"; `render.ergo` catches `lookup` on its own line.
- **An operation cannot share its name with a type of the provider's package** (G-ex-e-1, also hit by ssg). The operations are `ReadPage` and `ReadTemplate`, not `Page` and `Template`, because the test provider in `site` would declare functions named like the types `Page` and `Template`.
- **A parenthesised literal in an `if` header loses its parentheses in Go** (G-ex-c-2, also hit by ssg). The checker asks for `if x != (Some[string]{…}) {`, and the Go build then fails; `Build` binds the literal with `let` first.
- **`return f()` of a failure value is refused** (G-ex-a-15, also hit by ssg). Each arm of `run` spells `os.Exit{Code: 1, Message: …}`.
- **`return` cannot leave a `with` block** (G-ex-d-10, also hit by ssg). Two test helpers exist only to hold a catch with a `return`.
- **No slice of a slice** (G-ex-f-1, also hit by ssg). `memory.Disk.MkdirAll` builds each prefix of a path by a loop.
- **A long failure row is spelled in each signature** (G-ex-a-4, G-ex-c-14, also hit by ssg). `Build`'s row has ten names; `Render`'s five appear again in `nodes`.
- **The property generator has no markup bytes** (G-ex-d-4, also hit by ssg). The escaping laws map the generated text through `Spice`, and `SpiceReachesEveryMarkupByte` shows that it reaches each of the five bytes.
- **`Append` may share the array** (G-ex-d-14, also hit by ssg). `extend` copies the include chain before it appends.
- **A `let` of a variant takes the variant's type** (G-ex-b-16, also hit by ssg in a probe): a `Node` is written `let n Node = Each{…}`.
- **A failed `assert` shows no values** (G-ex-b-2, also hit by ssg). The text of a `flag` failure was found by reading std `flag`.

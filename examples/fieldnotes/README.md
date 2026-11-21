# fieldnotes

A field journal as a static web site (SSR WEB2, layer 1). It reads one note a file from a folder (`entries/SLUG.txt` by default), and builds an index, a page per entry, an RSS feed and a 404 page with `modules/web`'s prerender host, `web/static`. `fieldnotes build OUT` writes the tree under `OUT`, ready for any host that serves files (Cloudflare's static assets, with `404.html` as its not-found page). `fieldnotes print` prints each file of it. The site's links and pages are the same whichever host serves them: a built file is the body `web.Fetch` answers for its route (law `BuiltEqualsServed`).

```text
title: A walk to the marsh
day: 2026-10-03
related: heron-count, first-frost

The first paragraph, which may run
over several lines.

The second paragraph.
```

The head is `key: value` lines up to the first blank line. `title` and `day` (YYYY-MM-DD) are required, and `related` names other entries by slug. A file's name is its entry's slug: lower-case letters, digits and `-`.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and checked by hand against the notes in `testdata/`. It requires one module of this repository, `modules/web` (`go.mod`, with a `replace` to its folder), and std's `xml`.

## Layout

| folder | what it holds |
| --- | --- |
| `journal/` | The domain. `Entry`, `Missing`, the contract `Journal` (an operation type with laws `ListedEntriesRead` and `ListIsNewestFirst`), `Same`, `Before`, `Slug` with law `SlugIsItsOwn`. Pure. |
| `content/` | The note format. `note.ergo`: the machine `Reader`, whose state is the part being read (`Head` or `Body`), `Step`, `Read`, `Format`, `IsDay`, and `ReadFolder` over `fs.Files`, with law `FormatThenRead` beside `Format`. `tests.ergo`: each bad note's line and reason. |
| `memory/` | `Journal`, the `journal.Journal` of a list of entries (`NewJournal`), on which the contract's laws run. |
| `site/` | The site. `route.ergo`: `Route = Index \| Entry \| Feed`, its `web.Routes` map, and law `HrefParses` (the link form: a route parses back from the link `web.To` prints). `page.ergo`: `Page`, `Load` (needs `journal.Journal`), `Listed`, `New`, with the laws `BuiltEqualsServed`, `LinksResolve`, `EveryPageIsListed`, `FeedIsWellFormed` over generated journals. `view.ergo`: `View`, with a `match` over every page. `feed.ergo`: `FeedText`. |
| `cmd/fieldnotes/` | The binary. It binds the host's files, standard output and arguments, and a `memory.Journal` of the notes it read, and is the only place that chooses them. Its test builds over an `fstest.Memory`. |

## Cases

`print` (every file of the site from `testdata/entries`), `build` (the files written), `bad-note` (a day that is not one), `broken-link` (an entry related to no entry: the build stops instead of writing a dead link), `usage`.

## What it shows

- A route map as a closed union with `Href` and `Parse`. A link is `html.Href(web.To(site.Map, Entry{Slug: s}))`, so a link to a route that does not exist does not compile.
- A loader whose `needs` row is the journal and nothing else. Its typed failure `journal.Missing` has a status page (`New`'s `Status`, an exhaustive `match`), and in a build it stops the build.
- The note format read by a machine.
- The host law: what the static host writes is what `Fetch` serves.

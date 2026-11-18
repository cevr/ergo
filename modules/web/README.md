# web

An Ergo module of this repository (`modules/`, MK0): a web framework for server-rendered sites (SSR WEB1). A site is a route map, a loader and a view, and `Fetch` serves it as a function from a request to a response, with no server. A host (a prerender, a native server, a Worker) and a test call the same `Fetch`.

| Package | What |
| --- | --- |
| `web` | `Routes[R]` with law `HrefParses`; `To` (a route's link, for `html.Href`); `Site[R, P, F, N]`; `Reply`; `Fetch`; `Request` |
| `web/webtest` | `Get`, `Head` and `Post` on a site with no server |

## A site

- **Routes** are a closed union `R`. The site's `Routes[R]` gives `Href(r)`, a route's place (`ui.URL`), and `Parse(u)`, the route a place is, or `None`. Law `HrefParses`: `Parse(Href(r)) == Some{r}` for every route, run for every implementation. A link is `html.Href(web.To(m, Post{Slug: s}))`, so a link to a route that does not exist does not compile. A segment written with `ui.PathOf` is escaped, so a slug with `/`, `?` or `%` round-trips. An empty segment is skipped by `ui.Segments`, so a route that holds an empty slug parses from its parent's place (the fixture's `note{Slug: ""}` is `/notes`).
- **The loader** `Load(r R) P fails F needs N` is what a route's page shows. Its `fails` row is the site's typed failures; its `needs` row is what a page may read, bound in a context by `cmd/<name>/main.ergo` or by a test.
- **The view** `View(p P) html.Node` is pure.
- **The status table** `Status(f F) Reply` is a function whose `match` over `F` is exhaustive, so a failure variant with no page and status does not compile (the diagnostics case `cmd/ergo/testdata/diagnostics/web-failure-without-status`).
- **The 404 page** `NotFound(path)`.

## Fetch

| Request | Answer |
| --- | --- |
| a path that parses to no route, any method | `NotFound(path)`, status 404 |
| GET of a route | `View(Load(r))`, status 200; a failure `f` is `Status(f)`'s page and status |
| HEAD | GET's status and header, no body |
| any other method on a route | 405, `Allow: GET, HEAD`, no body |

Each answer is `Content-Type: text/html; charset=utf-8` but the 405. The request's path is read as written, escaped: `Fetch` does not unescape it before `Parse` (`ui.Segments` unescapes each segment).

Laws (`app.ergo`, over a site of their own whose loader fails): `HeadIsGetWithoutBody`, `UnknownPathIsNotFound`, `OtherMethodIsNotAllowed`, `KnownPathIsLoadThenView`. `webtest`'s test `FixtureSiteAnswers` drives a three-route site whose loader reads a capability.

## Decided

- `web` is a module, not std: everything it uses any module may use (STD HAS NO PRIVILEGES). Reverse by promoting it after two sites use it unchanged.
- Routes are ordinary code over segments, with no pattern language (Go-like local readability). Reverse by a `web.Pattern` library.
- A site is a record of functions, not an operation type: its loader's capability row `N` is a row parameter of `Site`, so a site's `needs` reaches `Fetch`'s row and a host's. Reverse by nothing: an operation type cannot carry an implementation's row.
- WEB1 answers pages only (GET, HEAD). Forms, actions, redirects and `webtest.Follow` are WEB4's.

# web

An Ergo module of this repository (`modules/`, MK0): a web framework for server-rendered sites (SSR WEB1). A site is a route map, a loader and a view, and `Fetch` serves it as a function from a request to a response, with no server. A host (a prerender, a native server, a Worker) and a test call the same `Fetch`.

| Package | What |
| --- | --- |
| `web` | `Routes[R]` with law `HrefParses`; `To` (a route's link, for `html.Href`); `Site[R, P, F, N]`; `Reply`; `Fetch`; `Request`; `Asset`, `Hashed`, `Stylesheet`, `Link`; `Row`, `HrefOf`, `ParseOf` (route tables) |
| `web/webtest` | `Get`, `Head` and `Post` on a site with no server; `Links` (the hrefs of a page's `<a>` elements) and `Unresolved` (each link to the site that does not answer 200) |
| `web/static` | the prerender host: `PathOf` (a place's file), `Pages`, `Build` (writes them under a folder, `needs fs.Writes`), `Differences` (what a build writes that `Fetch` would not serve) |

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

## Route tables

A route map may be data: a constant `[]web.Row` of `{Variant, Pattern}`, one row per variant of the route union, read by `HrefOf` and `ParseOf` through the union's derived codec, so a tool reads the map without running a body. A pattern is a path of static segments and `:key` segments, then an optional query part: `"/posts/:slug"`, `"/search?q&limit&desc"`. A key is the field's wire key. A path key's value is one escaped segment; a query key is read by `http.DecodeQuery`'s rule, as its field's type (`limit=x` is no place of an int limit, a scalar given twice is none, a `json.Optional` may be absent), and a key the pattern does not list is not read. A site's map calls them: `func Href(_ Map, r Route) ui.URL { web.HrefOf(table, r) }` and `func Parse(_ Map, u ui.URL) Option[Route] { web.ParseOf[Route](table, u) }`, so `Routes`' law `HrefParses` holds over the table as over a hand-written map. `Fetch` parses a request's path with its query, so a route's query state reaches its loader. Laws: `TableRouteIsServed`, `QueryIsTyped`.

## Assets

A site's `Assets` are files served as they are beside its pages: a style sheet, a font, an image. `Fetch` answers a GET of an asset's `Path` with its bytes and its `Content-Type` (HEAD with no body, another method 405), before it parses the path as a route; `static` writes each at its place's file. `Hashed(name, type, body)` places an asset at `/stem.<12 hex digits of its SHA-256>.ext`, so a changed file gets a new place and a host or browser may keep it forever; `Stylesheet(name, css)` is a `text/css` one; `Link(a)` is its link for `html.Href`. Laws: `HashedNameIsItsBytes`, `AssetServedIsItsBytes`, and static's `StaticEqualsServed`, whose law site has an asset.

## The static host

`static.Build(site, routes, dir)` writes a file for each listed route (`/` is `index.html`, `/entries/a` is `entries/a/index.html`, a last segment with a `.` is that file) and the 404 page as `404.html`. A route whose loader fails stops the build with its failure; a place with a `.` or `..` segment, or a `/` inside one, has no file (`BadPath`). Laws: `StaticEqualsServed` (each file is the body `Fetch` answers for a GET of its route, and nothing else is written) and `ListedRoutesLoad`. `examples/fieldnotes` runs both over its own site.

## Decided

- `web` is a module, not std: everything it uses any module may use (STD HAS NO PRIVILEGES). Reverse by promoting it after two sites use it unchanged.
- Routes are ordinary code over segments, with no pattern language (Go-like local readability). Reverse by a `web.Pattern` library.
- A site is a record of functions, not an operation type: its loader's capability row `N` is a row parameter of `Site`, so a site's `needs` reaches `Fetch`'s row and a host's. Reverse by nothing: an operation type cannot carry an implementation's row.
- WEB1 answers pages only (GET, HEAD). Forms, actions, redirects and `webtest.Follow` are WEB4's.

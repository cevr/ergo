# TOML 1.0 conformance data

Source: https://github.com/toml-lang/toml-test at `96fd0884`.
`corpus.json` preserves each `.toml` selected by `tests/files-toml-1.0.0`,
its name and source, and its corresponding JSON value for valid documents.
Invalid documents have no `want`. Source bytes are base64 in `raw`, so the
invalid UTF-8 encoding probes retain their exact bytes too.
The upstream MIT license is in `LICENSE`. No parser code is copied.

`runtime/v1.TestTOMLCorpus` reads every row; it pins no count or digest.

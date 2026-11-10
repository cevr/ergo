# One key across formats

The config package declares its encoded field keys once. JSON encoding and
TOML decoding use those same keys. The binary chooses no host provider: it
prints a fixed config. `SameRecordAcrossFormats` tests generated configs
against independently written JSON and TOML documents.

Measured by the focused example run recorded in the unit request; no speed
claim. The matched Go form is a struct with explicit encoding/json tags and
a TOML decoder with the same tags, not a runtime naming policy.

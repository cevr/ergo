# modelslice

Cuts a committed slice of a provider's Smithy model with module clientgen's `smithy.Prune`: the named operations and the shapes they reach, with a named list of traits dropped and the provenance written into the slice's metadata. The slices under `modules/clientgen/smithy/testdata` are made by this binary and no other tool.

```text
$ modelslice s3-2006-03-01.json s3.json <source URL> <note> drop:smithy.rules#endpointTests com.amazonaws.s3#GetObject
s3.json: 20 of 828 shapes, 61234 bytes
```

# modelslice

Cuts a committed slice of a provider's Smithy model with module clientgen's `smithy.Prune`: the named operations and the shapes they reach, with a named list of traits dropped and the provenance written into the slice's metadata. The slices under `modules/clientgen/testdata/models` are made by this binary and no other tool.

```text
$ modelslice s3-2006-03-01.json s3.json <source URL> <note> drop:smithy.rules#endpointTests drop:smithy.rules#endpointBdd com.amazonaws.s3#GetObject com.amazonaws.s3#PutObject com.amazonaws.s3#DeleteObject com.amazonaws.s3#ListObjectsV2 com.amazonaws.s3#HeadObject
s3.json: 131 of 828 shapes
```

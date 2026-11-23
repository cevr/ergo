# clientgen

Cuts committed slices of a provider's Smithy model with module clientgen's `smithy.Prune`, and writes provider clients from them with `emit.Emit`.

`slice` keeps the named operations and the shapes they reach, with a named list of traits dropped and the provenance written into the slice's metadata. The slices under `modules/clientgen/testdata/models` are made by this binary and no other tool.

```text
$ clientgen slice s3-2006-03-01.json s3.json <source URL> <note> drop:smithy.rules#endpointTests drop:smithy.rules#endpointBdd com.amazonaws.s3#GetObject com.amazonaws.s3#PutObject com.amazonaws.s3#DeleteObject com.amazonaws.s3#ListObjectsV2 com.amazonaws.s3#HeadObject
s3.json: 131 of 828 shapes
```

`emit` writes the client package of a provider it names (`s3`: HeadObject, GetObject, PutObject, DeleteObject) into a folder; `--check` writes nothing and fails when the folder's files are not what emit writes. The case `s3-current` runs it over the committed slice and `modules/aws/s3`, the drift guard until a gen line can run an Ergo program. The provider `s3-list` adds ListObjectsV2, which emit refuses by name until the XML wire rules land (case `s3-list`).

```text
$ clientgen emit s3 modules/clientgen/testdata/models/s3.json modules/aws/s3
modules/aws/s3: wrote 1 files
```

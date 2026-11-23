set -eu
mkdir -p base .work/base/.work-trash/a.10 .work/other/.work-trash/x.1
printf '[work]\n' > base/work.toml
printf 'put\t"a"\t".work/base/.work-trash/a.10"\t"base"\t"main"\t"copy"\ttrashed\t10\n' > .work/base/.registry
printf 'put\t"b"\t".work/base/b"\t"base"\t"main"\t""\tcreating\t990\n' >> .work/base/.registry

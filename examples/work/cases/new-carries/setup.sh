set -e
mkdir base
cd base
git init -q -b main .
printf 'hello\n' > a.txt
printf 'dist\nnode_modules\n' > .gitignore
git add a.txt .gitignore
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
mkdir -p node_modules/left-pad node_modules/.bin dist
printf 'module.exports = 1\n' > node_modules/left-pad/index.js
ln -s ../left-pad/index.js node_modules/.bin/left-pad
printf 'built\n' > dist/out.js
printf 'draft\n' > notes.txt
git update-index -q --refresh
cd ..
mkdir -p .work/base
: > .work/base/.registry
# The hook shows the clone from inside: what it carries (installed
# packages with their symlink, ignored output, an untracked file), its
# Git status (the marker is not in it), the marker, the stat settings of a
# clone made file by file and the untracked cache every clone keeps, and
# its index byte for byte the base's (the branch at HEAD wrote no index;
# compared before git status, which writes the untracked cache into it).
cmd='{ cmp -s .git/index ../../../base/.git/index && echo same-index; ls -A; readlink node_modules/.bin/left-pad; cat dist/out.js; git status --porcelain; cat .git/work-id; git config core.checkStat; git config core.trustctime; git config core.untrackedCache; echo "$WORK_ID $WORK_SOURCE $WORK_DESTINATION"; echo base:; git -C ../../../base status --porcelain; } >&2; exit 3'
printf "[hooks]\npostcreate = ['%s']\n" "$cmd" > base/work.toml
printf 'hooks.postcreate: %s' "$cmd" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-64 > .work/base/.trusted

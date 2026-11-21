set -e
# A derived class's command runs only after work trust: this work.toml
# was never allowed, so derive runs nothing (the command would have
# written ran.txt) and says which commands wait.
mkdir base
cd base
git init -q -b main .
printf '[[class]]\nmatch = "gen.txt"\nkind = "derived"\nby = "touch ran.txt"\n' > work.toml
git add work.toml
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
cd ..
mkdir -p .work/base
: > .work/base/.registry
cp -a base .work/base/w
printf 'put\t"w"\t".work/base/w"\t"base"\t"main"\t"reflink"\tactive\t900\t""\t""\t"w1"\t""\n' >> .work/base/.registry

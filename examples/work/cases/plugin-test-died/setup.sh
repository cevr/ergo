set -e
# plugin-test of a plugin that dies (sh -c false: the command is split at
# white space; it ends with status 1 and writes
# nothing): it is refused by law Answers at the first file it was asked
# for, never admitted as a plugin with no units or no diagnostics.
mkdir base
cd base
git init -q -b main .
printf 'hello\n' > a.txt
git add a.txt
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init

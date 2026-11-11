mkdir -p site/css
printf '<h1>jobs</h1>\n' > site/index.html
printf 'h1 { color: teal; }\n' > site/css/site.css
printf 'outside the site\n' > secret.txt
ln -s ../secret.txt site/leak.txt

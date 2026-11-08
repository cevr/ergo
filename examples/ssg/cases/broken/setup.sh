cp -R "$1/../../testdata/site" site
echo '<nav>{{ for p in pages }}<a>{{ p.title }}</a>' > site/templates/nav.html

mkdir -p data
printf 'put\t"name"\t"Ada"\nput\t"lang"\t"Ergo"\nput\t"name"\t"Ada Lovelace"\ndel\t"lang"\nput\t"note"\t"two\\nlines"\n' > data/kv.log

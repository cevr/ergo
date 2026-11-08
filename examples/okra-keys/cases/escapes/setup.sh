mkdir -p .okra
printf '%s' ' {"q\u00e9":"v\n\"1234567890" , "b":"tab\there", "b":"again\ud83d\ude00x"} ' > .okra/keys.json

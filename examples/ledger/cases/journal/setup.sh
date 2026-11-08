#!/bin/sh
# A journal from an earlier day: alice opened with a deposit of $12.34.
printf 'open\t"alice"\t0\npost\t"d1"\t"world"\t"alice"\t1234\n' > bank.journal

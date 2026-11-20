// ENCDEC receipt controls. "hand": the write-through cache as a Go
// programmer writes it, reading back what it wrote (Quote, then Unquote of
// the line). "floor": the least work for the same result: the line is
// still quoted for the backing, and the cache keeps the value itself.
package main

import (
	"fmt"
	"os"
	"strconv"
)

type cache struct {
	backing map[string]string
	values  map[string]string
	writes  int
}

func (c *cache) setHand(key, value string) error {
	line := strconv.Quote(value)
	c.backing[key] = line
	c.writes++
	kept, err := strconv.Unquote(line)
	if err != nil {
		return err
	}
	c.values[key] = kept
	return nil
}

func (c *cache) setFloor(key, value string) {
	c.backing[key] = strconv.Quote(value)
	c.writes++
	c.values[key] = value
}

func main() {
	const n, k = 64, 200000
	c := &cache{backing: map[string]string{}, values: map[string]string{}}
	keys := make([]string, 0, n)
	for i := range n {
		keys = append(keys, "key"+strconv.Itoa(i))
	}
	for r := range k {
		value := "value \"" + strconv.Itoa(r) + "\"\twith some text to quote: abcdefgh"
		if os.Args[1] == "floor" {
			c.setFloor(keys[r%n], value)
		} else if err := c.setHand(keys[r%n], value); err != nil {
			panic(err)
		}
	}
	total := 0
	for _, key := range keys {
		total += len(c.values[key]) + len(c.backing[key])
	}
	fmt.Println(total, c.writes)
}

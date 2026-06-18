package main

import (
	"fmt"
	"os"

	"sigmaos/apps/pagerank"
)

func main() {
	fmt.Printf("Hello, world!\n")

	pagerank.Pagerank(os.Args[1:])
}

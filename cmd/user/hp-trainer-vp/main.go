package main

import (
	"os"

	"sigmaos/apps/hpsearch"
)

func main() {
	hpsearch.RunValueProcsTrainer(os.Args[1:])
}

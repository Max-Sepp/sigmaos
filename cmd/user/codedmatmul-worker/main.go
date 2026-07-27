package main

import (
	"os"

	"sigmaos/apps/codedmatmul"
)

func main() {
	codedmatmul.RunWorker(os.Args[1:])
}

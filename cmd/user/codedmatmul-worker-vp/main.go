package main

import (
	"os"

	"sigmaos/apps/codedmatmul"
)

func main() {
	codedmatmul.RunValueProcsWorker(os.Args[1:])
}

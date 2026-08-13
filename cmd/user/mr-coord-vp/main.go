package main

import (
	"os"

	"sigmaos/apps/mr"
)

func main() {
	mr.RunValueProcsCoord(os.Args[1:])
}

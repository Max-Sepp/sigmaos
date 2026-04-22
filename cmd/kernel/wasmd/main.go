package main

import (
	"os"

	db "sigmaos/debug"
	wasmsrv "sigmaos/proxy/wasm/srv"
)

func main() {
	if len(os.Args) != 2 {
		db.DFatalf("Usage: %v kernelId\nPassed: %v", os.Args[0], os.Args)
	}
	kernelId := os.Args[1]
	if err := wasmsrv.RunWASMSrv(kernelId); err != nil {
		db.DFatalf("Fatal start: %v %v\n", os.Args[0], err)
	}
}

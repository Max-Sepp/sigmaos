// Command valuesched runs the per-realm value-proc scheduling service.
package main

import (
	db "sigmaos/debug"
	"sigmaos/valueprocs/adapter"
)

func main() {
	if err := adapter.RunSrv(); err != nil {
		db.DFatalf("valuesched: %v", err)
	}
}

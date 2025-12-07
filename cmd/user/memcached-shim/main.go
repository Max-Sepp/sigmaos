package main

import (
	"os"
	"strconv"
	"time"

	"sigmaos/apps/memcached"
	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/util/perf"
)

func main() {
	if len(os.Args) != 3 {
		db.DFatalf("Usage: %v snapPn port", os.Args[0])
	}
	pe := proc.GetProcEnv()
	execTimeStr := os.Getenv("SIGMA_EXEC_TIME")
	// If not set, bail out
	if execTimeStr == "" {
		return
	}
	execTimeMicro, err := strconv.ParseInt(execTimeStr, 10, 64)
	if err != nil {
		db.DFatalf("Error parsing exec time 2: %v", err)
		return
	}
	execTime := time.UnixMicro(execTimeMicro)
	perf.LogSpawnLatency("Setup.RuntimeInit+Isolation", pe.GetPID(), pe.GetSpawnTime(), execTime)
	snapPn := os.Args[1]
	port := os.Args[2]
	if err := memcached.RunMemcachedShim(snapPn, port); err != nil {
		db.DFatalf("Start %v err %v", os.Args[0], err)
	}
}

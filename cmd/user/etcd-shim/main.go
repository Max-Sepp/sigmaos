package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"sigmaos/apps/etcd"
	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/util/perf"
)

func main() {
	if len(os.Args) != 6 {
		db.DFatalf("Usage: %v snapPn name listen-peer-urls advertise-client-urls listen-client-urls", os.Args[0])
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
	name := os.Args[2]
	peerUrls := strings.Split(os.Args[3], ",")
	clientUrls := strings.Split(os.Args[4], ",")
	listenClientUrls := strings.Split(os.Args[5], ",")
	if err := etcd.RunEtcdShim(snapPn, name, peerUrls, clientUrls, listenClientUrls); err != nil {
		db.DFatalf("Start %v err %v", os.Args[0], err)
	}
}

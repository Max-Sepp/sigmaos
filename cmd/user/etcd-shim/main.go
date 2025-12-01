package main

import (
	"os"
	"strings"

	"sigmaos/apps/etcd"
	db "sigmaos/debug"
)

func main() {
	if len(os.Args) != 5 {
		db.DFatalf("Usage: %v name listen-peer-urls advertise-client-urls listen-client-urls", os.Args[0])
	}
	name := os.Args[1]
	peerUrls := strings.Split(os.Args[2], ",")
	clientUrls := strings.Split(os.Args[3], ",")
	listenClientUrls := strings.Split(os.Args[4], ",")
	if err := etcd.RunEtcdShim(name, peerUrls, clientUrls, listenClientUrls); err != nil {
		db.DFatalf("Start %v err %v", os.Args[0], err)
	}
}

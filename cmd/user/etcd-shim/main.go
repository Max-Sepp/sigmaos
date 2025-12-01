package main

import (
	"os"
	"strings"

	"sigmaos/apps/etcd"
	db "sigmaos/debug"
)

func main() {
	if len(os.Args) != 6 {
		db.DFatalf("Usage: %v snapPn name listen-peer-urls advertise-client-urls listen-client-urls", os.Args[0])
	}
	snapPn := os.Args[1]
	name := os.Args[2]
	peerUrls := strings.Split(os.Args[3], ",")
	clientUrls := strings.Split(os.Args[4], ",")
	listenClientUrls := strings.Split(os.Args[5], ",")
	if err := etcd.RunEtcdShim(snapPn, name, peerUrls, clientUrls, listenClientUrls); err != nil {
		db.DFatalf("Start %v err %v", os.Args[0], err)
	}
}

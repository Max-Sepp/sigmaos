package etcd

import (
	"os"
	"os/exec"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/etcdutl/v3/snapshot"
	"go.uber.org/zap"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmasrv"
)

type EtcdShim struct {
	clnt *clientv3.Client
	ssrv *sigmasrv.SigmaSrv
}

func RunEtcdShim(snapPn, name string, peerUrls, clientUrls, listenClientUrls []string) error {
	es := &EtcdShim{}
	// Otherwise, don't post EP (and instead post EP in the EP cache service)
	ssrv, err := sigmasrv.NewSigmaSrv("", es, proc.GetProcEnv())
	if err != nil {
		db.DFatalf("Err NewSigmaSrv: %v", err)
		return err
	}
	es.ssrv = ssrv
	// Restore the snapshot to a fresh etcd directory
	if err := es.restoreSnapshot(snapPn, name, peerUrls); err != nil {
		db.DFatalf("Err restoreSnapshot: %v", err)
		return err
	}
	// Start etcd
	cmd, err := startEtcd(name, peerUrls, clientUrls, listenClientUrls)
	if err != nil {
		db.DFatalf("Err startEtcd: %v", err)
		return err
	}
	// Create a client to etcd
	clnt, err := newEtcdClnt(clientUrls)
	if err != nil {
		db.DFatalf("Err newEtcdClnt: %v", err)
		return err
	}
	es.clnt = clnt
	// Mark etcd as started
	if err := ssrv.MemFs.SigmaClnt().Started(); err != nil {
		db.DFatalf("Err Started: %v", err)
		return err
	}
	db.DPrintf(db.ETCD, "Started shim and etcd")
	// Wait for eviction
	if err := ssrv.MemFs.SigmaClnt().WaitEvict(ssrv.SigmaClnt().ProcEnv().GetPID()); err != nil {
		db.DFatalf("Err WaitEvict: %v", err)
		return err
	}
	db.DPrintf(db.ETCD, "Evicted shim, killing etcd")
	// Evicted, so kill etcd
	if err := cmd.Process.Kill(); err != nil {
		db.DFatalf("Err Kill: %v", err)
		return err
	}
	if err := cmd.Wait(); err != nil {
		db.DPrintf(db.ETCD, "Etcd exited with err: %v", err)
	}
	db.DPrintf(db.ETCD, "Exiting etcd shim")
	return ssrv.SrvExit(proc.NewStatus(proc.StatusEvicted))
}

func (es *EtcdShim) restoreSnapshot(snapPn string, name string, peerUrls []string) error {
	// Download the snapshot file
	b, err := es.ssrv.MemFs.SigmaClnt().GetFile(snapPn)
	if err != nil {
		db.DPrintf(db.ETCD_ERR, "Err GetFile: %v", err)
		db.DPrintf(db.ERROR, "Err GetFile: %v", err)
		return err
	}
	// Write the snapshot out
	localSnapPn := "./snapshot.db"
	if err := os.WriteFile(localSnapPn, b, 0777); err != nil {
		db.DPrintf(db.ETCD_ERR, "Err WriteFile: %v", err)
		db.DPrintf(db.ERROR, "Err WriteFile: %v", err)
		return err
	}
	snapshotMgr := snapshot.NewV3(zap.L())
	rc := snapshot.RestoreConfig{
		SnapshotPath:        localSnapPn,
		Name:                name,
		OutputDataDir:       "",
		OutputWALDir:        "",
		PeerURLs:            peerUrls,
		InitialCluster:      name + "=" + strings.Join(peerUrls, ","),
		InitialClusterToken: "etcd-cluster-1",
		SkipHashCheck:       true,
	}
	return snapshotMgr.Restore(rc)
}

func startEtcd(name string, peerUrls, clientUrls, listenClientUrls []string) (*exec.Cmd, error) {
	// Build the etcd command arguments
	args := []string{
		"--name", name,
		"--initial-advertise-peer-urls", strings.Join(peerUrls, ","),
		"--listen-peer-urls", strings.Join(peerUrls, ","),
		"--advertise-client-urls", strings.Join(clientUrls, ","),
		"--listen-client-urls", strings.Join(listenClientUrls, ","),
		"--initial-cluster-state", "new",
		"--initial-cluster-token", "sigmaos-etcd-cluster",
	}

	// Create the etcd command
	cmd := exec.Command("etcd-v1.0", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Start etcd in the background
	if err := cmd.Start(); err != nil {
		db.DPrintf(db.ERROR, "Failed to start etcd: %v", err)
		return nil, err
	}

	db.DPrintf(db.ETCD, "Started etcd with name %s, peer URLs: %v, client URLs: %v", name, peerUrls, clientUrls)
	return cmd, nil
}

func newEtcdClnt(endpoints []string) (*clientv3.Client, error) {
	clnt, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
		//		DialOptions: []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		//			ep, ok := etcdMnts[addr]
		//			// Check that the endpoint is in the map
		//			if !ok {
		//				db.DFatalf("Unknown fsetcd endpoint proto: addr %v eps %v", addr, etcdMnts)
		//			}
		//			return dial(sp.NewEndpointFromProto(ep))
		//		})},
	})
	if err != nil {
		return nil, err
	}
	return clnt, nil
}

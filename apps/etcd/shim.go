package etcd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/etcdutl/v3/snapshot"
	"go.uber.org/zap"

	db "sigmaos/debug"
	"sigmaos/proc"
	s3clnt "sigmaos/proxy/s3/clnt"
	sp "sigmaos/sigmap"
	"sigmaos/sigmasrv"
	"sigmaos/util/perf"
)

type EtcdShim struct {
	clnt   *clientv3.Client
	ssrv   *sigmasrv.SigmaSrv
	s3Clnt *s3clnt.S3Clnt
}

func RunEtcdShim(snapPn, name string, peerUrls, clientUrls, listenClientUrls []string) error {
	es := &EtcdShim{}
	pe := proc.GetProcEnv()
	// Otherwise, don't post EP (and instead post EP in the EP cache service)
	ssrv, err := sigmasrv.NewSigmaSrv("", es, pe)
	if err != nil {
		db.DFatalf("Err NewSigmaSrv: %v", err)
		return err
	}
	es.ssrv = ssrv
	start := time.Now()
	// Create an S3 clnt
	s3Clnt, err := s3clnt.NewS3ClntInit(es.ssrv.SigmaClnt().FsLib, filepath.Join(sp.S3, pe.GetKernelID()), pe.GetRunBootScript())
	if err != nil {
		db.DFatalf("Err newS3Clnt: %v", err)
	}
	es.s3Clnt = s3Clnt
	perf.LogSpawnLatency("Initialization.ConnectionSetup", pe.GetPID(), pe.GetSpawnTime(), start)
	start = time.Now()
	// Restore the snapshot to a fresh etcd directory
	if err := es.restoreSnapshot(snapPn, name, peerUrls); err != nil {
		db.DFatalf("Err restoreSnapshot: %v", err)
		return err
	}
	perf.LogSpawnLatency("Initialization.LoadState", pe.GetPID(), pe.GetSpawnTime(), start)
	start = time.Now()
	// Start etcd
	cmd, err := startEtcd(name, peerUrls, clientUrls, listenClientUrls)
	if err != nil {
		db.DFatalf("Err startEtcd: %v", err)
		return err
	}
	perf.LogSpawnLatency("Initialization.StartEtcd", pe.GetPID(), pe.GetSpawnTime(), start)
	// Create a client to etcd
	clnt, err := newEtcdClnt(clientUrls)
	if err != nil {
		db.DFatalf("Err newEtcdClnt: %v", err)
		return err
	}
	es.clnt = clnt
	perf.LogSpawnLatency("Initialization.NewEtcdClnt", pe.GetPID(), pe.GetSpawnTime(), start)
	// Mark etcd as started
	if err := ssrv.SigmaClnt().Started(); err != nil {
		db.DFatalf("Err Started: %v", err)
		return err
	}
	db.DPrintf(db.ETCD, "Started shim and etcd")
	// Wait for eviction
	if err := ssrv.SigmaClnt().WaitEvict(ssrv.SigmaClnt().ProcEnv().GetPID()); err != nil {
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
	pn := strings.Split(snapPn, "/")
	bucket := pn[0]
	key := filepath.Join(pn[1:]...)
	var b []byte
	var err error
	start := time.Now()
	if es.ssrv.SigmaClnt().ProcEnv().GetRunBootScript() {
		b, err = es.s3Clnt.DelegatedGetObject(0)
		if err != nil {
			db.DPrintf(db.ETCD_ERR, "Err DelegatedGetObject bucket:%v key:%v: %v", bucket, key, err)
			db.DPrintf(db.ERROR, "Err DelegatedGetObject bucket:%v key:%v: %v", bucket, key, err)
			return err
		}
		db.DPrintf(db.ETCD, "Done delegated get")
	} else {
		b, err = es.s3Clnt.GetObject(bucket, key)
		if err != nil {
			db.DPrintf(db.ETCD_ERR, "Err GetObject bucket:%v key:%v: %v", bucket, key, err)
			db.DPrintf(db.ERROR, "Err GetObject bucket:%v key:%v: %v", bucket, key, err)
			return err
		}
		db.DPrintf(db.ETCD, "Done direct get")
	}
	perf.LogSpawnLatency("Initialization.DownloadState", es.ssrv.SigmaClnt().ProcEnv().GetPID(), es.ssrv.SigmaClnt().ProcEnv().GetSpawnTime(), start)
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

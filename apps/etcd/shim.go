package etcd

import (
	"os"
	"os/exec"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmasrv"
)

type EtcdShim struct {
	clnt *clientv3.Client
}

func RunEtcdShim(name string, peerUrls, clientUrls, listenClientUrls []string) error {
	es := &EtcdShim{}
	// Otherwise, don't post EP (and instead post EP in the EP cache service)
	ssrv, err := sigmasrv.NewSigmaSrv("", es, proc.GetProcEnv())
	if err != nil {
		db.DFatalf("Err NewSigmaSrv: %v", err)
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

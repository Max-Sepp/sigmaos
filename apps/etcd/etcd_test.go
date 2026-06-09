package etcd_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	clientv3 "go.etcd.io/etcd/client/v3"

	epsrv "sigmaos/apps/epcache/srv"
	"sigmaos/apps/etcd"
	db "sigmaos/debug"
	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/test"
)

func TestCompile(t *testing.T) {
}

func TestGenSnapshot(t *testing.T) {
	const (
		PEER_PORT = 6380
		CLNT_PORT = 6379
		SNAP_PATH = "/tmp/snapshot-20MB.db"
		N_KV      = 200000
	)

	// Get the project root directory dynamically
	cwd, err := os.Getwd()
	if !assert.Nil(t, err, "Failed to get working directory: %v", err) {
		return
	}
	// Navigate up to project root (assuming we're in apps/etcd/)
	projectRoot := filepath.Join(cwd, "..", "..")
	etcdBinary := filepath.Join(projectRoot, "bin", "user", "etcd-v1.0")

	// Start etcd server
	etcdCmd := exec.Command(etcdBinary,
		"--name", "etcd-proc",
		"--initial-advertise-peer-urls", fmt.Sprintf("http://127.0.0.1:%d", PEER_PORT),
		"--listen-peer-urls", fmt.Sprintf("http://127.0.0.1:%d", PEER_PORT),
		"--advertise-client-urls", fmt.Sprintf("http://127.0.0.1:%d", CLNT_PORT),
		"--listen-client-urls", fmt.Sprintf("http://127.0.0.1:%d", CLNT_PORT),
		"--initial-cluster-state", "new",
		"--initial-cluster-token", "test-cluster",
	)
	etcdCmd.Stdout = os.Stdout
	etcdCmd.Stderr = os.Stderr

	if err := etcdCmd.Start(); !assert.Nil(t, err, "Failed to start etcd: %v", err) {
		return
	}
	defer etcdCmd.Process.Kill()

	// Wait for etcd to be ready
	time.Sleep(2 * time.Second)

	// Create etcd client
	clnt, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{fmt.Sprintf("http://127.0.0.1:%d", CLNT_PORT)},
		DialTimeout: 5 * time.Second,
	})
	if !assert.Nil(t, err, "Failed to create etcd client: %v", err) {
		return
	}
	defer clnt.Close()

	// Put some test keys and values
	ctx := context.Background()
	for i := 0; i < N_KV; i++ {
		key := fmt.Sprintf("key-%v", i)
		val := fmt.Sprintf("val-%v", i)
		_, err := clnt.Put(ctx, key, val)
		if !assert.Nil(t, err, "Failed to put key %s: %v", key, err) {
			return
		}
		if i%1000 == 0 {
			db.DPrintf(db.TEST, "Putting key-value pairs: %v/%v", i, N_KV)
		}
	}
	db.DPrintf(db.TEST, "Put %v key-value pairs", N_KV)

	// Generate snapshot
	snapshot, err := clnt.Snapshot(ctx)
	defer snapshot.Close()
	if !assert.Nil(t, err, "Failed to generate snapshot : %v", err) {
		return
	}

	// Save snapshot to file
	snapFile, err := os.Create(SNAP_PATH)
	if !assert.Nil(t, err, "Failed to create snapshot file: %v", err) {
		return
	}
	defer snapFile.Close()

	written, err := snapFile.ReadFrom(snapshot)
	if !assert.Nil(t, err, "Failed to write snapshot: %v", err) {
		return
	}

	db.DPrintf(db.TEST, "Snapshot saved to %s (%d bytes)", SNAP_PATH, written)
	t.Logf("Successfully generated snapshot at %s with %d bytes", SNAP_PATH, written)
}

func TestEtcd(t *testing.T) {
	const (
		SNAP_S3_PATH = "name/s3/~local/9ps3/snapshot-10MB.db"
		SNAP_UX_PATH = "name/ux/~local/snapshot-10MB.db"
		USE_UX       = false
	)

	// Only works when running with gVisor
	if !assert.True(t, test.UseGVisor, "Needs to run with GVisor") {
		return
	}

	mrts, err1 := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err1, "Error New Tstate: %v", err1) {
		return
	}
	defer mrts.Shutdown()

	ts := mrts.GetRealm(test.REALM1)

	// Create EPCache job
	epcj, err := epsrv.NewEPCacheJob(ts.SigmaClnt)
	if !assert.Nil(t, err, "Err new EPCacheJob: %v", err) {
		return
	}
	defer epcj.Stop()

	// Create etcd job config
	etcdCfg := etcd.NewEtcdJobConfig(
		"etcd-job",
		SNAP_S3_PATH,
		SNAP_UX_PATH,
		"etcd-proc",
		6380, // peer port
		6379, // client port
		true, // use init script
		USE_UX,
		proc.Tmcpu(1000),
	)

	// Create etcd job
	etcdJob, err := etcd.NewEtcdJob(etcdCfg, ts.SigmaClnt, epcj)
	if !assert.Nil(t, err, "Err new etcd job: %v", err) {
		return
	}

	db.DPrintf(db.TEST, "Starting etcd job")
	// Start etcd
	err = etcdJob.Start(sp.NOT_SET)
	if !assert.Nil(t, err, "Err start etcd: %v", err) {
		return
	}
	db.DPrintf(db.TEST, "Etcd started")

	time.Sleep(5 * time.Second)

	db.DPrintf(db.TEST, "Stopping etcd job")
	// Stop etcd
	err = etcdJob.Stop()
	if !assert.Nil(t, err, "Err stop etcd: %v", err) {
		return
	}
	db.DPrintf(db.TEST, "Etcd stopped")
}

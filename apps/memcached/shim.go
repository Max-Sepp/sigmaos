package memcached

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bradfitz/gomemcache/memcache"

	db "sigmaos/debug"
	"sigmaos/proc"
	s3clnt "sigmaos/proxy/s3/clnt"
	sp "sigmaos/sigmap"
	"sigmaos/sigmasrv"
	"sigmaos/util/perf"
)

const (
	TMPFS_MOUNT      = "/tmp/memcached"
	SNAP_FILE        = "snapshot"
	MEMCACHED_MEM_SZ = "40" // Min mem sz for memcached is 39 MB
)

type MemcachedShim struct {
	clnt   *memcache.Client
	ssrv   *sigmasrv.SigmaSrv
	s3Clnt *s3clnt.S3Clnt
}

func RunMemcachedShim(snapPn string, port string) error {
	ms := &MemcachedShim{}
	pe := proc.GetProcEnv()
	// Create SigmaSrv
	ssrv, err := sigmasrv.NewSigmaSrv("", ms, pe)
	if err != nil {
		db.DFatalf("Err NewSigmaSrv: %v", err)
		return err
	}
	ms.ssrv = ssrv
	start := time.Now()
	// Create an S3 clnt
	s3Clnt, err := s3clnt.NewS3Clnt(ms.ssrv.SigmaClnt().FsLib, filepath.Join(sp.S3, pe.GetKernelID()))
	if err != nil {
		db.DFatalf("Err newS3Clnt: %v", err)
	}
	ms.s3Clnt = s3Clnt
	perf.LogSpawnLatency("Initialization.ConnectionSetup", pe.GetPID(), pe.GetSpawnTime(), start)
	start = time.Now()
	// Restore the snapshot
	start2, err := ms.restoreSnapshot(snapPn)
	if err != nil {
		db.DFatalf("Err restoreSnapshot: %v", err)
		return err
	}
	perf.LogSpawnLatency("Initialization.LoadState", pe.GetPID(), pe.GetSpawnTime(), start)
	start = time.Now()
	// Start memcached
	cmd, err := startMemcached(port)
	if err != nil {
		db.DFatalf("Err startMemcached: %v", err)
		return err
	}
	perf.LogSpawnLatency("Initialization.StartMemcached", pe.GetPID(), pe.GetSpawnTime(), start)
	// Create a client to etcd
	clnt, err := newMemcachedClnt(port)
	if err != nil {
		db.DFatalf("Err newMemcachedClnt: %v", err)
		return err
	}
	ms.clnt = clnt
	perf.LogSpawnLatency("Initialization.NewMemcachedClnt", pe.GetPID(), pe.GetSpawnTime(), start)
	perf.LogSpawnLatency("Paper.Initialization.AppLoadState", pe.GetPID(), pe.GetSpawnTime(), start2)
	// Mark memcached as started
	if err := ssrv.SigmaClnt().Started(); err != nil {
		db.DFatalf("Err Started: %v", err)
		return err
	}
	db.DPrintf(db.MEMCACHED, "Started shim and memcached")
	// Wait for eviction
	if err := ssrv.SigmaClnt().WaitEvict(ssrv.SigmaClnt().ProcEnv().GetPID()); err != nil {
		db.DFatalf("Err WaitEvict: %v", err)
		return err
	}
	db.DPrintf(db.MEMCACHED, "Evicted shim, killing memcached")
	// Evicted, so kill memcached
	if err := cmd.Process.Kill(); err != nil {
		db.DFatalf("Err Kill: %v", err)
		return err
	}
	if err := cmd.Wait(); err != nil {
		db.DPrintf(db.MEMCACHED, "Memcached exited with err: %v", err)
	}
	db.DPrintf(db.MEMCACHED, "Exiting memcached shim")
	return ssrv.SrvExit(proc.NewStatus(proc.StatusEvicted))
}

func newMemcachedClnt(port string) (*memcache.Client, error) {
	clnt := memcache.New("127.0.0.1:" + port)
	return clnt, nil
}

func (ms *MemcachedShim) restoreSnapshot(snapPn string) (time.Time, error) {
	pn := strings.Split(snapPn, "/")
	bucket := pn[0]
	key := filepath.Join(pn[1:]...)
	var b []byte
	var err error
	start := time.Now()
	pe := proc.GetProcEnv()
	if ms.ssrv.SigmaClnt().ProcEnv().GetRunBootScript() {
		b, err = ms.s3Clnt.DelegatedGetObject(0)
		if err != nil {
			db.DPrintf(db.MEMCACHED_ERR, "Err DelegatedGetObject bucket:%v key:%v: %v", bucket, key, err)
			db.DPrintf(db.ERROR, "Err DelegatedGetObject bucket:%v key:%v: %v", bucket, key, err)
			return time.Now(), err
		}
		db.DPrintf(db.MEMCACHED, "Done delegated get")
		perf.LogSpawnLatency("Paper.Initialization.TransferState", pe.GetPID(), pe.GetSpawnTime(), start)
		perf.LogSpawnLatency("Initialization.TransferState", pe.GetPID(), pe.GetSpawnTime(), start)
	} else {
		b, err = ms.s3Clnt.GetObject(bucket, key)
		if err != nil {
			db.DPrintf(db.MEMCACHED_ERR, "Err GetObject bucket:%v key:%v: %v", bucket, key, err)
			db.DPrintf(db.ERROR, "Err GetObject bucket:%v key:%v: %v", bucket, key, err)
			return time.Now(), err
		}
		db.DPrintf(db.MEMCACHED, "Done direct get")
		perf.LogSpawnLatency("Paper.Initialization.DownloadState", pe.GetPID(), pe.GetSpawnTime(), start)
	}
	// Get meta file
	b2, err := ms.s3Clnt.GetObject(bucket, key+".meta")
	if err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err GetObject bucket:%v key:%v: %v", bucket, key+".meta", err)
		db.DPrintf(db.ERROR, "Err GetObject bucket:%v key:%v: %v", bucket, key+".meta", err)
		return time.Now(), err
	}
	db.DPrintf(db.MEMCACHED, "Done direct get")
	perf.LogSpawnLatency("Initialization.DownloadState", pe.GetPID(), pe.GetSpawnTime(), start)
	start = time.Now()
	// Create tmpfs mount (shim always runs in Docker/gVisor)
	//	if err := MakeTmpfs(TMPFS_MOUNT); err != nil {
	if err := os.Mkdir(TMPFS_MOUNT, 0777); err != nil {
		return time.Now(), err
	}
	// Write the snapshot out to a local file
	localSnapPn := filepath.Join(TMPFS_MOUNT, SNAP_FILE)
	if err := os.WriteFile(localSnapPn, b, 0777); err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err WriteFile: %v", err)
		db.DPrintf(db.ERROR, "Err WriteFile: %v", err)
		return time.Now(), err
	}
	if err := os.WriteFile(localSnapPn+".meta", b2, 0777); err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err WriteFile: %v", err)
		db.DPrintf(db.ERROR, "Err WriteFile: %v", err)
		return time.Now(), err
	}
	db.DPrintf(db.MEMCACHED, "Restored snapshot to %v", localSnapPn)
	return start, nil
}

func startMemcached(port string) (*exec.Cmd, error) {
	// Build the memcached command arguments
	// -u: must specify that memcached runs as root user, because it runs in the
	// Docker container
	// -p: port to listen on
	// -m: max memory in MB (default 64)
	// -c: max simultaneous connections (default 1024)
	// -e: mmap file (in a tmpfs) from which to load warm start snapshot
	args := []string{
		"-u", "root",
		"-p", port,
		"-m", MEMCACHED_MEM_SZ,
		"-c", "1024",
		"-e", filepath.Join(TMPFS_MOUNT, SNAP_FILE),
		"-v", // verbose mode
	}
	// TODO: turn off verbose mode

	// Create the memcached command
	cmd := exec.Command("memcached-v1.0", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Start memcached in the background
	if err := cmd.Start(); err != nil {
		db.DPrintf(db.ERROR, "Failed to start memcached: %v", err)
		return nil, err
	}

	db.DPrintf(db.MEMCACHED, "Started memcached on port %s", port)
	return cmd, nil
}

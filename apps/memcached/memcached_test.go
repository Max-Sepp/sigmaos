package memcached_test

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/stretchr/testify/assert"

	"sigmaos/apps/memcached"
	db "sigmaos/debug"
	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/test"
)

func TestGenSnapshot(t *testing.T) {
	const (
		PORT              = "11211"
		N_KV              = 10000
		SNAPSHOT_SAVE_DIR = "/tmp/memcached-snapshot"
	)

	// Delete and create the snapshot save directory
	_ = os.RemoveAll(SNAPSHOT_SAVE_DIR)
	if err := os.MkdirAll(SNAPSHOT_SAVE_DIR, 0755); !assert.Nil(t, err, "Failed to create snapshot save directory: %v", err) {
		return
	}

	// Remove any old tmpfs mount first
	_ = memcached.RemoveTmpfs(memcached.TMPFS_MOUNT, false) // Ignore errors if not mounted

	// Create tmpfs mount (test runs on host, not in Docker)
	if err := memcached.MakeTmpfs(memcached.TMPFS_MOUNT, false); !assert.Nil(t, err, "Failed to create tmpfs: %v", err) {
		return
	}
	defer func() {
		// Copy all tmpfs directory contents to persistent directory before removing
		copyDirContents(memcached.TMPFS_MOUNT, SNAPSHOT_SAVE_DIR)
		db.DPrintf(db.TEST, "Copied tmpfs contents to %s", SNAPSHOT_SAVE_DIR)
		memcached.RemoveTmpfs(memcached.TMPFS_MOUNT, false)
	}()

	// Get the project root directory dynamically
	cwd, err := os.Getwd()
	if !assert.Nil(t, err, "Failed to get working directory: %v", err) {
		return
	}
	// Navigate up to project root (assuming we're in apps/memcached/)
	projectRoot := filepath.Join(cwd, "..", "..")
	memcachedBinary := filepath.Join(projectRoot, "bin", "user", "memcached-v1.0")
	args := []string{
		"-p", PORT,
		"-m", memcached.MEMCACHED_MEM_SZ_DEFAULT,
		"-c", "1024",
		"-e", filepath.Join(memcached.TMPFS_MOUNT, memcached.SNAP_FILE), // Point memcached at the tmpfs mount snapshot file
		"-v", // verbose mode
	}

	// Start memcached server
	memcachedCmd := exec.Command(memcachedBinary, args...)
	memcachedCmd.Stdout = os.Stdout
	memcachedCmd.Stderr = os.Stderr

	if err := memcachedCmd.Start(); !assert.Nil(t, err, "Failed to start memcached: %v", err) {
		return
	}
	defer memcachedCmd.Process.Kill()

	// Wait for memcached to be ready
	time.Sleep(2 * time.Second)

	// Create memcached client
	clnt := memcache.New("127.0.0.1:" + PORT)

	// Put some test keys and values
	for i := 0; i < N_KV; i++ {
		key := fmt.Sprintf("key-%v", i)
		val := fmt.Sprintf("val-%v", i)
		err := clnt.Set(&memcache.Item{Key: key, Value: []byte(val)})
		if !assert.Nil(t, err, "Failed to set key %s: %v", key, err) {
			return
		}
		if i%1000 == 0 {
			db.DPrintf(db.TEST, "Setting key-value pairs: %v/%v", i, N_KV)
		}
	}
	db.DPrintf(db.TEST, "Set %v key-value pairs", N_KV)

	// Send SIGUSR1 signal to memcached process to trigger snapshot save
	if err := memcachedCmd.Process.Signal(syscall.SIGUSR1); !assert.Nil(t, err, "Failed to send SIGUSR1 to memcached: %v", err) {
		return
	}
	db.DPrintf(db.TEST, "Sent SIGUSR1 to memcached process")

	// Give memcached time to write the snapshot
	time.Sleep(1 * time.Second)

	// Read the snapshot file from tmpfs
	snapData, err := os.ReadFile(filepath.Join(memcached.TMPFS_MOUNT, memcached.SNAP_FILE))
	if !assert.Nil(t, err, "Failed to read snapshot from tmpfs: %v", err) {
		return
	}

	db.DPrintf(db.TEST, "Snapshot read from tmpfs (%d bytes)", len(snapData))
	t.Logf("Successfully generated snapshot with %d bytes", len(snapData))
}

// copyDirContents copies all files from src directory to dst directory
func copyDirContents(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			// Create subdirectory and recursively copy contents
			if err := os.MkdirAll(dstPath, 0755); err != nil {
				return err
			}
			if err := copyDirContents(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			// Copy file
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies a single file from src to dst
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	// Copy file permissions
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, srcInfo.Mode())
}

func TestMemcached(t *testing.T) {
	const (
		PORT             = 11211
		SNAP_S3_PATH     = "name/s3/~local/9ps3/memcached-snapshot-40M"
		SNAP_UX_PATH     = "name/ux/~local/memcached-snapshot-40M"
		USE_INITSCRIPT   = true
		USE_UX           = false
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

	db.DPrintf(db.TEST, "Resolved snapshot pathnames: s3=%v ux=%v", SNAP_S3_PATH, SNAP_UX_PATH)

	// Create memcached job config
	conf := memcached.NewMemcachedJobConfig("memcached-job", SNAP_S3_PATH, SNAP_UX_PATH, PORT, USE_INITSCRIPT, USE_UX, proc.Tmcpu(1000))
	db.DPrintf(db.TEST, "Created memcached job config: %v", conf)

	// Create memcached job
	job, err := memcached.NewMemcachedJob(conf, mrts.GetRealm(test.REALM1).SigmaClnt, nil)
	if !assert.Nil(t, err, "Err NewMemcachedJob: %v", err) {
		return
	}
	db.DPrintf(db.TEST, "Created memcached job")

	// Start the memcached job
	db.DPrintf(db.TEST, "Pre start")
	err = job.Start(sp.NOT_SET)
	if !assert.Nil(t, err, "Err Start: %v", err) {
		return
	}
	db.DPrintf(db.TEST, "Post start")

	// Let it run for a bit
	time.Sleep(5 * time.Second)

	// Stop the memcached job
	db.DPrintf(db.TEST, "Pre stop")
	err = job.Stop()
	if !assert.Nil(t, err, "Err Stop: %v", err) {
		return
	}
	db.DPrintf(db.TEST, "Post stop")

	db.DPrintf(db.TEST, "Successfully started and stopped memcached job")
}

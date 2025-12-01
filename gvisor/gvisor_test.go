package gvisor_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	db "sigmaos/debug"
	"sigmaos/gvisor"
	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/test"
)

func TestCompile(t *testing.T) {
}

func TestConfig(t *testing.T) {
	p := proc.NewProc("/bin/bash", []string{"-c", "echo 'hello world'"})
	cfg := gvisor.NewDefaultConfig(p)
	db.DPrintf(db.TEST, "gvisor config: %v", cfg)
}

func TestOverlayDir(t *testing.T) {
	baseBundleDir := gvisor.BASE_BUNDLE_PATH
	pid := sp.Tpid("test-proc-pid")

	// Create the proc bundle overlay filesystem
	err := gvisor.MakeProcBundleOverlayFS()
	assert.Nil(t, err, "Failed to create proc bundle overlay filesystem: %v", err)
	defer os.RemoveAll(gvisor.PROC_BUNDLE_OVERLAY_DIR)

	// Get the overlay bundle directory path for this pid
	overlayBundleDir := gvisor.PidToOverlayBundleDirPath(pid)

	// Create the overlay directory
	err = gvisor.CreateBundleOverlay(baseBundleDir, overlayBundleDir, false)
	assert.Nil(t, err, "Failed to create bundle overlay: %v", err)

	// Check that the merged directory exists
	mergedDir := filepath.Join(overlayBundleDir, "merged")
	entries, err := os.ReadDir(mergedDir)
	assert.Nil(t, err, "Failed to read merged directory: %v", err)
	db.DPrintf(db.TEST, "Merged directory contents: %v", entries)

	// Check that config.json does not exist in the merged directory
	configPath := filepath.Join(mergedDir, "config.json")
	_, err = os.Stat(configPath)
	assert.True(t, os.IsNotExist(err), "config.json should not exist in merged directory")

	// Create config.json in the overlay directory
	p := proc.NewProc("/bin/bash", []string{"-c", "echo 'overlay test'"})
	cfg := gvisor.NewDefaultConfig(p)
	err = cfg.WriteToFile(mergedDir)
	assert.Nil(t, err, "Failed to write config file: %v", err)

	// Verify config.json was created
	_, err = os.Stat(configPath)
	assert.Nil(t, err, "config.json should exist after writing: %v", err)

	// Destroy the overlay directory
	err = gvisor.DestroyBundleOverlay(overlayBundleDir, false)
	assert.Nil(t, err, "Failed to destroy bundle overlay: %v", err)

	// Verify the overlay directory is gone
	_, err = os.Stat(overlayBundleDir)
	assert.True(t, os.IsNotExist(err), "Overlay directory should not exist after destruction")
}

func TestHelloWorld(t *testing.T) {
	baseBundleDir := gvisor.BASE_BUNDLE_PATH
	pid := sp.Tpid("test-proc-pid")
	containerID := "hello_world_ctr"

	// Create the proc bundle overlay filesystem
	err := gvisor.MakeProcBundleOverlayFS()
	assert.Nil(t, err, "Failed to create proc bundle overlay filesystem: %v", err)
	defer os.RemoveAll(gvisor.PROC_BUNDLE_OVERLAY_DIR)

	// Get the overlay bundle directory path for this pid
	overlayBundleDir := gvisor.PidToOverlayBundleDirPath(pid)

	// Create the overlay bundle
	err = gvisor.CreateBundleOverlay(baseBundleDir, overlayBundleDir, false)
	assert.Nil(t, err, "Failed to create bundle overlay: %v", err)
	defer gvisor.DestroyBundleOverlay(overlayBundleDir, false)

	// Create and write default config to overlay bundle directory
	mergedDir := filepath.Join(overlayBundleDir, "merged")
	p := proc.NewProc("/bin/bash", []string{"-c", "echo 'hello world' ; sleep 100s"})
	cfg := gvisor.NewDefaultConfigBinPath(p, "/bin/bash")
	err = cfg.WriteToFile(mergedDir)
	assert.Nil(t, err, "Failed to write config file: %v", err)

	// Run the container asynchronously with shared stdout
	runCmd := exec.Command("sudo", "runsc", "--network=host", "run", "--bundle", mergedDir, containerID)
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	start := time.Now()
	err = runCmd.Start()
	assert.Nil(t, err, "Failed to start container: %v", err)
	time.Sleep(2 * time.Second)

	// Get container state
	stateCmd := exec.Command("sudo", "runsc", "state", containerID)
	out, err := stateCmd.CombinedOutput()
	assert.Nil(t, err, "Failed to get container state: %v", err)
	db.DPrintf(db.TEST, "Container state:\n%s", string(out))

	// Kill the container
	killCmd := exec.Command("sudo", "runsc", "kill", containerID)
	out, err = killCmd.CombinedOutput()
	assert.Nil(t, err, "Failed to kill container: %v", err)
	db.DPrintf(db.TEST, "Container killed: %s", string(out))

	// Wait for the container to complete
	err = runCmd.Wait()
	assert.NotNil(t, err, "Container run not killed: %v", err)
	db.DPrintf(db.TEST, "Container completed")
	assert.True(t, time.Since(start) < 20*time.Second, "Waited too long %v", time.Since(start))
}

func TestEtcd(t *testing.T) {
	mrts, err1 := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err1, "Error New Tstate: %v", err1) {
		return
	}
	defer mrts.Shutdown()

	const (
		PEER_PORT = 6380
		CLNT_PORT = 6379
	)

	a := proc.NewProc("etcd", []string{
		fmt.Sprintf("--name=%v", "etcd-proc"),
		fmt.Sprintf("--listen-peer-urls=http://127.0.0.1:%v", PEER_PORT),
		fmt.Sprintf("--advertise-client-urls=http://127.0.0.1:%v", CLNT_PORT),
		fmt.Sprintf("--listen-client-urls=http://127.0.0.1:%v", CLNT_PORT),
	})
	a.SetMcpu(1000)
	db.DPrintf(db.TEST, "Pre spawn")
	err := mrts.GetRealm(test.REALM1).Spawn(a)
	assert.Nil(t, err, "Spawn")
	db.DPrintf(db.TEST, "Post spawn")

	db.DPrintf(db.TEST, "Pre waitexit")
	status, err := mrts.GetRealm(test.REALM1).WaitExit(a.GetPid())
	db.DPrintf(db.TEST, "Post waitexit")
	assert.Nil(t, err, "WaitExit error")
	assert.True(t, status != nil && status.IsStatusOK(), "Exit status wrong: %v", status)
}

package gvisor_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	db "sigmaos/debug"
	"sigmaos/gvisor"
)

func TestCompile(t *testing.T) {
}

func TestConfig(t *testing.T) {
	ctrCmd := []string{"/bin/bash", "-c", "echo 'hello world'"}
	cfg := gvisor.NewDefaultConfig(ctrCmd)
	db.DPrintf(db.TEST, "gvisor config: %v", cfg)
}

func TestHelloWorld(t *testing.T) {
	baseBundleDir := "/home/arielck/workspace/hack/gvisor/bundle"
	overlayBundleDir := "/tmp/gvisor-hello-world-overlay"
	containerID := "hello_world_ctr"

	// Create the overlay bundle
	err := gvisor.CreateBundleOverlay(baseBundleDir, overlayBundleDir)
	assert.Nil(t, err, "Failed to create bundle overlay: %v", err)
	defer gvisor.DestroyBundleOverlay(overlayBundleDir)

	// Create and write default config to overlay bundle directory
	mergedDir := filepath.Join(overlayBundleDir, "merged")
	ctrCmd := []string{"/bin/bash", "-c", "echo 'hello world' ; sleep 100s"}
	cfg := gvisor.NewDefaultConfig(ctrCmd)
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

func TestOverlayDir(t *testing.T) {
	baseBundleDir := "/home/arielck/workspace/hack/gvisor/bundle"
	overlayBundleDir := "/tmp/gvisor-overlay-test"

	// Create the overlay directory
	err := gvisor.CreateBundleOverlay(baseBundleDir, overlayBundleDir)
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
	ctrCmd := []string{"/bin/bash", "-c", "echo 'overlay test'"}
	cfg := gvisor.NewDefaultConfig(ctrCmd)
	err = cfg.WriteToFile(mergedDir)
	assert.Nil(t, err, "Failed to write config file: %v", err)

	// Verify config.json was created
	_, err = os.Stat(configPath)
	assert.Nil(t, err, "config.json should exist after writing: %v", err)

	// Destroy the overlay directory
	err = gvisor.DestroyBundleOverlay(overlayBundleDir)
	assert.Nil(t, err, "Failed to destroy bundle overlay: %v", err)

	// Verify the overlay directory is gone
	_, err = os.Stat(overlayBundleDir)
	assert.True(t, os.IsNotExist(err), "Overlay directory should not exist after destruction")
}

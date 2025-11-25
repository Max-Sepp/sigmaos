package gvisor_test

import (
	"os"
	"os/exec"
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
	bundleDir := "/home/arielck/workspace/hack/gvisor/bundle"
	containerID := "hello_world_ctr"

	// Create and write default config to bundle directory
	ctrCmd := []string{"/bin/bash", "-c", "echo 'hello world' ; sleep 100s"}
	cfg := gvisor.NewDefaultConfig(ctrCmd)
	err := cfg.WriteToFile(bundleDir)
	assert.Nil(t, err, "Failed to write config file: %v", err)

	// Run the container asynchronously with shared stdout
	runCmd := exec.Command("sudo", "runsc", "--network=host", "run", "--bundle", bundleDir, containerID)
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

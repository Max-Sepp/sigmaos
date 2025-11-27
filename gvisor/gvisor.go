package gvisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	db "sigmaos/debug"
	"sigmaos/proc"
)

type GVisorContainer struct {
	p             *proc.Proc
	containerID   string
	runCmd        *exec.Cmd
	overlayDir    string
	baseBundleDir string
}

func StartGVisorContainer(p *proc.Proc, baseBundleDir string) (*GVisorContainer, error) {
	containerID := "gvisor-ctr-" + p.GetPid().String()
	// Get the overlay bundle directory path for this pid
	overlayBundleDir := PidToOverlayBundleDirPath(p.GetPid())
	// Create the overlay bundle
	if err := CreateBundleOverlay(baseBundleDir, overlayBundleDir); err != nil {
		db.DPrintf(db.ERROR, "[%v] Failed to create bundle overlay: %v", p.GetPid(), err)
		return nil, fmt.Errorf("[%v] failed to create bundle overlay: %v", p.GetPid(), err)
	}
	// Create and write default config to overlay bundle directory
	mergedDir := filepath.Join(overlayBundleDir, "merged")
	cfg := NewDefaultConfig(p.GetArgs())
	if err := cfg.WriteToFile(mergedDir); err != nil {
		DestroyBundleOverlay(overlayBundleDir)
		db.DPrintf(db.ERROR, "[%v] Failed to write config file: %v", p.GetPid(), err)
		return nil, fmt.Errorf("[%v] failed to write config file: %v", p.GetPid(), err)
	}
	// Run the container asynchronously with shared stdout
	runCmd := exec.Command("sudo", "runsc", "--network=host", "run", "--bundle", mergedDir, containerID)
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	if err := runCmd.Start(); err != nil {
		DestroyBundleOverlay(overlayBundleDir)
		db.DPrintf(db.ERROR, "[%v] Failed to start container: %v", p.GetPid(), err)
		return nil, fmt.Errorf("[%v] failed to start container: %v", p.GetPid(), err)
	}
	return &GVisorContainer{
		p:             p,
		containerID:   containerID,
		runCmd:        runCmd,
		overlayDir:    overlayBundleDir,
		baseBundleDir: baseBundleDir,
	}, nil
}

func (gc *GVisorContainer) String() string {
	return fmt.Sprintf("&{pid: %v, containerID: %v, overlayDir: %v}",
		gc.p.GetPid(), gc.containerID, gc.overlayDir)
}

func (gc *GVisorContainer) Wait() error {
	// Wait for the container to complete
	if err := gc.runCmd.Wait(); err != nil {
		db.DPrintf(db.GVISOR, "[%v] Container run completed with error: %v", gc.p.GetPid(), err)
		return err
	}
	db.DPrintf(db.GVISOR, "[%v] Container completed successfully", gc.p.GetPid())
	return nil
}

func (gc *GVisorContainer) Kill() error {
	// Kill the container
	killCmd := exec.Command("sudo", "runsc", "kill", gc.containerID)
	out, err := killCmd.CombinedOutput()
	if err != nil {
		db.DPrintf(db.ERROR, "[%v] Failed to kill container %v: %v, output: %s", gc.p.GetPid(), gc.containerID, err, string(out))
		return fmt.Errorf("[%v] failed to kill container: %v", gc.p.GetPid(), err)
	}
	db.DPrintf(db.GVISOR, "[%v] Container killed: %s", gc.p.GetPid(), string(out))
	// Wait for the container to complete
	if err := gc.runCmd.Wait(); err != nil {
		db.DPrintf(db.GVISOR, "[%v] Container run completed with error (expected after kill): %v", gc.p.GetPid(), err)
	}
	// Destroy the overlay bundle
	if err := DestroyBundleOverlay(gc.overlayDir); err != nil {
		db.DPrintf(db.ERROR, "[%v] Failed to destroy overlay bundle %v: %v", gc.p.GetPid(), gc.overlayDir, err)
		return fmt.Errorf("[%v] failed to destroy overlay bundle: %v", gc.p.GetPid(), err)
	}
	return nil
}

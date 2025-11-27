package gvisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	db "sigmaos/debug"
	sp "sigmaos/sigmap"
)

const (
	PROC_BUNDLE_OVERLAY_DIR = "/tmp/sigmaos-proc-bundle-overlays"
)

func MakeProcBundleOverlayFS() error {
	// Create the directory to hold procs' overlay bundles
	if err := os.Mkdir(PROC_BUNDLE_OVERLAY_DIR, 0777); err != nil {
		if !os.IsExist(err) {
			db.DPrintf(db.ERROR, "Err failed to create overlay bundle directory: %v", err)
			return fmt.Errorf("failed to create overlay bundle directory: %v", err)
		}
	}
	return nil
}

func PidToOverlayBundleDirPath(pid sp.Tpid) string {
	return filepath.Join(PROC_BUNDLE_OVERLAY_DIR, pid.String())
}

func CreateBundleOverlay(baseBundleDirPath string, overlayBundleDirPath string) error {
	// Create the overlay bundle directory
	if err := os.Mkdir(overlayBundleDirPath, 0755); err != nil {
		return fmt.Errorf("failed to create overlay bundle directory: %v", err)
	}

	// Create directories for overlay filesystem
	upperDir := filepath.Join(overlayBundleDirPath, "upper")
	workDir := filepath.Join(overlayBundleDirPath, "work")
	mergedDir := filepath.Join(overlayBundleDirPath, "merged")

	for _, dir := range []string{upperDir, workDir, mergedDir} {
		if err := os.Mkdir(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %v", dir, err)
		}
	}

	// Mount overlay filesystem
	mountOpts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", baseBundleDirPath, upperDir, workDir)
	mountCmd := exec.Command("sudo", "mount", "-t", "overlay", "overlay", "-o", mountOpts, mergedDir)
	if out, err := mountCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to mount overlay: %v, output: %s", err, string(out))
	}

	return nil
}

func DestroyBundleOverlay(overlayBundleDirPath string) error {
	// Unmount the overlay filesystem
	mergedDir := filepath.Join(overlayBundleDirPath, "merged")
	umountCmd := exec.Command("sudo", "umount", mergedDir)
	if out, err := umountCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to unmount overlay: %v, output: %s", err, string(out))
	}

	// Remove the overlay bundle directory
	if err := os.RemoveAll(overlayBundleDirPath); err != nil {
		return fmt.Errorf("failed to remove overlay bundle directory: %v", err)
	}

	return nil
}

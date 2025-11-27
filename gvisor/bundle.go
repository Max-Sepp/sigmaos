package gvisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func CreateBundleOverlay(baseBundleDirPath string, overlayBundleDirPath string) error {
	// Create the overlay bundle directory
	if err := os.Mkdir(overlayBundleDirPath, 0755); err != nil {
		return fmt.Errorf("failed to create overlay bundle directory: %w", err)
	}

	// Create directories for overlay filesystem
	upperDir := filepath.Join(overlayBundleDirPath, "upper")
	workDir := filepath.Join(overlayBundleDirPath, "work")
	mergedDir := filepath.Join(overlayBundleDirPath, "merged")

	for _, dir := range []string{upperDir, workDir, mergedDir} {
		if err := os.Mkdir(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Mount overlay filesystem
	mountOpts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", baseBundleDirPath, upperDir, workDir)
	mountCmd := exec.Command("sudo", "mount", "-t", "overlay", "overlay", "-o", mountOpts, mergedDir)
	if out, err := mountCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to mount overlay: %w, output: %s", err, string(out))
	}

	return nil
}

func DestroyBundleOverlay(overlayBundleDirPath string) error {
	// Unmount the overlay filesystem
	mergedDir := filepath.Join(overlayBundleDirPath, "merged")
	umountCmd := exec.Command("sudo", "umount", mergedDir)
	if out, err := umountCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to unmount overlay: %w, output: %s", err, string(out))
	}

	// Remove the overlay bundle directory
	if err := os.RemoveAll(overlayBundleDirPath); err != nil {
		return fmt.Errorf("failed to remove overlay bundle directory: %w", err)
	}

	return nil
}

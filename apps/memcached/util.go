package memcached

import (
	"os"
	"os/exec"

	db "sigmaos/debug"
)

// MakeTmpfs creates a directory and mounts a tmpfs filesystem at the specified path
// If inDocker is true, run mount without sudo (otherwise use sudo)
func MakeTmpfs(path string, inDocker bool) error {
	// Create tmpfs mount directory
	if err := os.MkdirAll(path, 0777); err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err MkdirAll: %v", err)
		db.DPrintf(db.ERROR, "Err MkdirAll: %v", err)
		return err
	}
	// Mount tmpfs
	var mountCmd *exec.Cmd
	if inDocker {
		mountCmd = exec.Command("mount", "-t", "tmpfs", "tmpfs", path)
	} else {
		mountCmd = exec.Command("sudo", "mount", "-t", "tmpfs", "tmpfs", path)
	}
	if out, err := mountCmd.CombinedOutput(); err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err mount tmpfs: %v, output: %s", err, string(out))
		db.DPrintf(db.ERROR, "Err mount tmpfs: %v, output: %s", err, string(out))
		return err
	}
	db.DPrintf(db.MEMCACHED, "Mounted tmpfs at %v", path)
	return nil
}

// RemoveTmpfs unmounts the tmpfs filesystem at the specified path and removes the directory
// If inDocker is true, run umount without sudo (otherwise use sudo)
func RemoveTmpfs(path string, inDocker bool) error {
	// Unmount tmpfs
	var unmountCmd *exec.Cmd
	if inDocker {
		unmountCmd = exec.Command("umount", path)
	} else {
		unmountCmd = exec.Command("sudo", "umount", path)
	}
	if out, err := unmountCmd.CombinedOutput(); err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err unmount tmpfs: %v, output: %s", err, string(out))
		return err
	}
	// Remove the directory
	if err := os.RemoveAll(path); err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err RemoveAll: %v", err)
		db.DPrintf(db.ERROR, "Err RemoveAll: %v", err)
		return err
	}
	db.DPrintf(db.MEMCACHED, "Removed tmpfs at %v", path)
	return nil
}

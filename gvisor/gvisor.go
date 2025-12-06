package gvisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/util/linux/mem"
)

type GVisorContainer struct {
	mu            sync.Mutex
	cond          *sync.Cond
	inDocker      bool
	p             *proc.Proc
	containerID   string
	runCmd        *exec.Cmd
	overlayDir    string
	baseBundleDir string
	goferPid      int
	sandboxExited bool
	runCmdErr     error
}

func StartGVisorContainer(p *proc.Proc, dialproxy bool, baseBundleDir string, inDocker bool) (*GVisorContainer, error) {
	db.DPrintf(db.CONTAINER, "RunUProc gvisor dialproxy %v %v env %v", dialproxy, p, os.Environ())
	containerID := "gvisor-ctr-" + p.GetPid().String()
	// Get the overlay bundle directory path for this pid
	overlayBundleDir := PidToOverlayBundleDirPath(p.GetPid())
	// Create the overlay bundle
	if err := CreateBundleOverlay(baseBundleDir, overlayBundleDir, inDocker); err != nil {
		db.DPrintf(db.ERROR, "[%v] Failed to create bundle overlay: %v", p.GetPid(), err)
		return nil, fmt.Errorf("[%v] failed to create bundle overlay: %v", p.GetPid(), err)
	}
	// Set some environemnt variables
	p.AppendEnv("PATH", "/bin:/bin2:/usr/bin:/home/sigmaos/bin/kernel:"+BIN_DIR)
	p.AppendEnv("SIGMA_EXEC_TIME", strconv.FormatInt(time.Now().UnixMicro(), 10))
	b, err := time.Now().MarshalText()
	if err != nil {
		db.DFatalf("Error marshal timestamp pb: %v", err)
	}
	p.AppendEnv("SIGMA_EXEC_TIME_PB", string(b))
	p.AppendEnv("SIGMA_SPAWN_TIME", strconv.FormatInt(p.GetSpawnTime().UnixMicro(), 10))
	p.AppendEnv(proc.SIGMAPERF, p.GetProcEnv().GetPerf())
	cfg := NewDefaultConfig(p)
	if inDocker {
		cfg.AddUserProcMounts()
	}
	// Create and write default config to overlay bundle directory
	mergedDir := filepath.Join(overlayBundleDir, "merged")
	if err := cfg.WriteToFile(mergedDir); err != nil {
		DestroyBundleOverlay(overlayBundleDir, inDocker)
		db.DPrintf(db.ERROR, "[%v] Failed to write config file: %v", p.GetPid(), err)
		return nil, fmt.Errorf("[%v] failed to write config file: %v", p.GetPid(), err)
	}
	// Run the container asynchronously with shared stdout
	var runCmd *exec.Cmd
	if inDocker {
		runCmd = exec.Command("runsc",
			"--host-uds=open", // Allow proc to connect to spproxyd
			"--debug",
			//			fmt.Sprintf("--debug-log=%v", filepath.Join(overlayBundleDir, "debug-log.txt")),
			"--ignore-cgroups",
			"--network=host",
			"run",
			//			fmt.Sprintf("--user-log=%v", filepath.Join(overlayBundleDir, "user-log.txt")),
			"--bundle",
			mergedDir,
			containerID)
	} else {
		runCmd = exec.Command("sudo",
			"runsc",
			"--ignore-cgroups",
			"--network=host",
			"run",
			"--bundle",
			mergedDir,
			containerID)
	}
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	if err := runCmd.Start(); err != nil {
		DestroyBundleOverlay(overlayBundleDir, inDocker)
		db.DPrintf(db.ERROR, "[%v] Failed to start container: %v", p.GetPid(), err)
		return nil, fmt.Errorf("[%v] failed to start container: %v", p.GetPid(), err)
	}
	gc := &GVisorContainer{
		inDocker:      inDocker,
		p:             p,
		containerID:   containerID,
		runCmd:        runCmd,
		overlayDir:    overlayBundleDir,
		baseBundleDir: baseBundleDir,
		goferPid:      0,
	}
	gc.cond = sync.NewCond(&gc.mu)
	go gc.waitForGoferPID()
	go gc.waitForRunCmd()
	return gc, nil
}

func (gc *GVisorContainer) Pid() int {
	return gc.GetGoferPid()
}

func (gc *GVisorContainer) SetGoferPid(pid int) {
	gc.mu.Lock()
	defer gc.mu.Unlock()

	gc.goferPid = pid
	gc.cond.Broadcast()
}

func (gc *GVisorContainer) GetGoferPid() int {
	gc.mu.Lock()
	defer gc.mu.Unlock()

	for gc.goferPid == 0 {
		gc.cond.Wait()
	}
	return gc.goferPid
}

func (gc *GVisorContainer) GetPSS() (proc.Tmem, error) {
	return mem.GetAggregatePSS(gc.runCmd.Process.Pid)
}

func (gc *GVisorContainer) String() string {
	return fmt.Sprintf("&{ pid:%v containerID:%v overlayDir:%v }",
		gc.p.GetPid(), gc.containerID, gc.overlayDir)
}

func (gc *GVisorContainer) Wait() error {
	gc.mu.Lock()
	defer gc.mu.Unlock()

	for !gc.sandboxExited {
		gc.cond.Wait()
	}
	return gc.runCmdErr
}

func (gc *GVisorContainer) Kill() error {
	// Kill the container
	var killCmd *exec.Cmd
	if gc.inDocker {
		killCmd = exec.Command("runsc", "kill", gc.containerID)
	} else {
		killCmd = exec.Command("sudo", "runsc", "kill", gc.containerID)
	}
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
	if err := DestroyBundleOverlay(gc.overlayDir, gc.inDocker); err != nil {
		db.DPrintf(db.ERROR, "[%v] Failed to destroy overlay bundle %v: %v", gc.p.GetPid(), gc.overlayDir, err)
		return fmt.Errorf("[%v] failed to destroy overlay bundle: %v", gc.p.GetPid(), err)
	}
	return nil
}

// Wait for the sandbox run command to complete
func (gc *GVisorContainer) waitForRunCmd() {
	// Wait for the container to complete
	err := gc.runCmd.Wait()
	if err != nil {
		db.DPrintf(db.GVISOR, "[%v] Container run completed with error: %v", gc.p.GetPid(), err)
	} else {
		db.DPrintf(db.GVISOR, "[%v] Container completed successfully", gc.p.GetPid())
	}
	gc.setExited(err)
}

func (gc *GVisorContainer) setExited(err error) {
	gc.mu.Lock()
	defer gc.mu.Unlock()

	gc.sandboxExited = true
	gc.runCmdErr = err
	gc.cond.Broadcast()
}

func (gc *GVisorContainer) exited() bool {
	gc.mu.Lock()
	defer gc.mu.Unlock()

	return gc.sandboxExited
}

func (gc *GVisorContainer) waitForGoferPID() {
	db.DPrintf(db.GVISOR, "[%v] Wait for gofer pid", gc.p.GetPid())
	var pid int
	var err error
	for {
		if gc.exited() {
			db.DPrintf(db.GVISOR_ERR, "[%v] Cancel wait for gofer pid", gc.p.GetPid())
			return
		}
		// TODO: handle sandbox exiting early due to error
		//		if gc.runCmd.Process.Exited() {
		//			db.DPrintf(db.GVISOR_ERR, "Bail out waiting for gofer PID: sandbox exited")
		//			return
		//		}
		pid, err = findGoferPID(gc.runCmd.Process.Pid)
		if err == nil {
			// The gofer PID has been found. Set it and return
			gc.SetGoferPid(pid)
			break
		}
		time.Sleep(5 * time.Millisecond)
		db.DPrintf(db.GVISOR, "[%v] Gofer PID not ready: %v", gc.p.GetPid(), err)
		continue
	}
	db.DPrintf(db.GVISOR, "[%v] Done wait for gofer pid: %v", gc.p.GetPid(), pid)
}

func findGoferPID(runscPid int) (int, error) {
	// Read all tasks in /proc/<runscPid>/task/
	taskDir := fmt.Sprintf("/proc/%d/task", runscPid)
	taskEntries, err := os.ReadDir(taskDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read task directory: %v", err)
	}
	// For each task, check its children
	for _, taskEntry := range taskEntries {
		if !taskEntry.IsDir() {
			continue
		}
		taskID := taskEntry.Name()
		childrenPath := filepath.Join(taskDir, taskID, "children")
		data, err := os.ReadFile(childrenPath)
		if err != nil {
			continue
		}
		childrenStr := string(data)
		if len(childrenStr) == 0 {
			continue
		}
		childrenStrs := strings.Split(strings.TrimSpace(childrenStr), " ")
		// Scan through all child PIDs and find the one with command "runsc-gofer"
		for _, childPidStr := range childrenStrs {
			childPid, err := strconv.Atoi(childPidStr)
			if err != nil {
				db.DPrintf(db.ERROR, "Error convert child pid to int: %v", err)
				continue
			}
			// Check if this PID's command is "runsc-gofer" to ensure this is the
			// gofer process
			cmdlinePath := fmt.Sprintf("/proc/%d/cmdline", childPid)
			cmdlineData, err := os.ReadFile(cmdlinePath)
			if err != nil {
				continue
			}
			cmdline := string(cmdlineData)
			if strings.HasPrefix(cmdline, "runsc-gofer") {
				return childPid, nil
			}
		}
	}
	return 0, fmt.Errorf("no runsc-gofer child found for runsc process %d", runscPid)
}

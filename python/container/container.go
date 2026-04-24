package container

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sched/msched/proc/srv/binsrv"
	sp "sigmaos/sigmap"
	"sigmaos/util/linux/mem"
	"sigmaos/util/perf"
)

const (
	PYTHON_BIN_DIR = "/home/sigmaos/bin/python"
)

type pyCmd struct {
	cmd *exec.Cmd
}

func (pc *pyCmd) Pid() int {
	return pc.cmd.Process.Pid
}

func (pc *pyCmd) GetPSS() (proc.Tmem, error) {
	return mem.GetAggregatePSS(pc.cmd.Process.Pid)
}

func (pc *pyCmd) Wait() error {
	return pc.cmd.Wait()
}

// StartPythonContainer execs python3 with the proc's program name resolved
// under BINFS. The script path is appended as the first argument so the proc
// can find itself, followed by the proc's own args.
func StartPythonContainer(uproc *proc.Proc) (*pyCmd, error) {
	scriptPath := filepath.Join(binsrv.BINFSMNT, uproc.GetVersionedProgram())

	args := append([]string{scriptPath}, uproc.GetArgs()...)
	cmd := exec.Command("python3", args...)
	//	cmd := exec.Command("valgrind", append([]string{"--trace-children=yes", "python3"}, args...)...)

	uproc.AppendEnv("PATH", "/bin:/usr/bin:/home/sigmaos/bin/kernel")
	uproc.AppendEnv("PYTHONPATH", "/home/sigmaos/python")
	uproc.AppendEnv("SIGMA_EXEC_TIME", strconv.FormatInt(time.Now().UnixMicro(), 10))
	b, err := time.Now().MarshalText()
	if err != nil {
		db.DFatalf("Error marshal timestamp: %v", err)
	}
	uproc.AppendEnv("SIGMA_EXEC_TIME_PB", string(b))
	uproc.AppendEnv("SIGMA_SPAWN_TIME", strconv.FormatInt(uproc.GetSpawnTime().UnixMicro(), 10))
	uproc.AppendEnv(proc.SIGMAPERF, uproc.GetProcEnv().GetPerf())
	cmd.Env = uproc.GetEnv()

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	db.DPrintf(db.CONTAINER, "StartPythonContainer %v args %v", scriptPath, args)

	s := time.Now()
	if err := cmd.Start(); err != nil {
		db.DPrintf(db.CONTAINER, "StartPythonContainer err %v: %v", cmd, err)
		return nil, err
	}
	perf.LogSpawnLatency("StartPythonContainer cmd.Start", uproc.GetPid(), uproc.GetSpawnTime(), s)
	return &pyCmd{cmd: cmd}, nil
}

// PythonBinPath returns the local filesystem path for a Python proc's script,
// used when the script is staged in PYTHON_BIN_DIR rather than served via BINFS.
func PythonBinPath(program string) string {
	return filepath.Join(PYTHON_BIN_DIR, program)
}

// CleanupPythonProc is a no-op placeholder; Python procs have no jail to tear
// down, but mirrors the scontainer.CleanupUProc call site for consistency.
func CleanupPythonProc(_ sp.Tpid) {}

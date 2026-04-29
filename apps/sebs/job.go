package sebs

import (
	"fmt"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
)

type SebsJobConfig struct {
	Benchmark  string     `json:"benchmark"`
	Event      string     `json:"event"`
	AsyncFetch bool       `json:"async_fetch"`
	Delegated  bool       `json:"delegated"`
	ShmemMB    proc.Tmem  `json:"shmem_mb"`
	Mcpu       proc.Tmcpu `json:"mcpu"`
}

func NewSebsJobConfig(benchmark, event string, asyncFetch, delegated bool, shmemMB proc.Tmem, mcpu proc.Tmcpu) *SebsJobConfig {
	return &SebsJobConfig{
		Benchmark:  benchmark,
		Event:      event,
		AsyncFetch: asyncFetch,
		Delegated:  delegated,
		ShmemMB:    shmemMB,
		Mcpu:       mcpu,
	}
}

type SebsJob struct {
	conf *SebsJobConfig
	*sigmaclnt.SigmaClnt
}

func NewSebsJob(conf *SebsJobConfig, sc *sigmaclnt.SigmaClnt) (*SebsJob, error) {
	return &SebsJob{conf: conf, SigmaClnt: sc}, nil
}

func (j *SebsJob) Run() (string, error) {
	args := []string{"--benchmark", j.conf.Benchmark, "--event", j.conf.Event}
	if j.conf.AsyncFetch {
		args = append(args, "--async-fetch")
	}
	if j.conf.Delegated {
		args = append(args, "--delegated")
	}
	p := proc.NewProc("sebs-runner.py", args)
	p.AddBin(fmt.Sprintf("%v-bundle.tar.gz", j.conf.Benchmark))
	p.GetProcEnv().UseSPProxy = true
	p.GetProcEnv().UseSPProxyProcClnt = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_PYTHON)
	if j.conf.Mcpu > 0 {
		p.SetMcpu(j.conf.Mcpu)
	}
	if j.conf.ShmemMB > 0 {
		p.SetShmemMB(j.conf.ShmemMB)
	}
	db.DPrintf(db.TEST, "SebsJob %v %v", j.conf.Benchmark, p.GetPid())
	if err := j.Spawn(p); err != nil {
		db.DPrintf(db.ERROR, "SebsJob Spawn err: %v", err)
		return "", err
	}
	if err := j.WaitStart(p.GetPid()); err != nil {
		db.DPrintf(db.ERROR, "SebsJob WaitStart err: %v", err)
		return "", err
	}
	status, err := j.WaitExit(p.GetPid())
	if err != nil {
		db.DPrintf(db.ERROR, "SebsJob WaitExit err: %v", err)
		return "", err
	}
	if !status.IsStatusOK() {
		return "", fmt.Errorf("sebs-runner.py [%v] exited with status: %v", j.conf.Benchmark, status)
	}
	return status.Msg(), nil
}

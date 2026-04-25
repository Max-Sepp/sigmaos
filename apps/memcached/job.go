package memcached

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	epsrv "sigmaos/apps/epcache/srv"
	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
)

const SHMEM_MB proc.Tmem = 42

type MemcachedJobConfig struct {
	Job           string     `json:"job"`
	SnapshotPath  string     `json:"snapshot_path"` // Path to snapshot file in SigmaOS
	Port          int        `json:"port"`
	UseCoSandbox bool       `json:"use_co_sandbox"`
	Mcpu          proc.Tmcpu `json:"mcpu"`
}

func NewMemcachedJobConfig(job, snapshotPath string, port int, useCoSandbox bool, mcpu proc.Tmcpu) *MemcachedJobConfig {
	return &MemcachedJobConfig{
		Job:           job,
		SnapshotPath:  snapshotPath,
		Port:          port,
		UseCoSandbox: useCoSandbox,
		Mcpu:          mcpu,
	}
}

type MemcachedJob struct {
	mu   sync.Mutex
	conf *MemcachedJobConfig
	*sigmaclnt.SigmaClnt
	EPCacheJob      *epsrv.EPCacheJob
	p               *proc.Proc
	coSandbox      []byte
	coSandboxInput []byte
	stopEPCJ        bool
}

func NewMemcachedJob(conf *MemcachedJobConfig, sc *sigmaclnt.SigmaClnt, epcj *epsrv.EPCacheJob) (*MemcachedJob, error) {
	stopEPCJ := false
	var err error
	// If not supplied, create epcache job
	if epcj == nil {
		stopEPCJ = true
		// Create epcache job
		epcj, err = epsrv.NewEPCacheJob(sc)
		if err != nil {
			db.DPrintf(db.ERROR, "Err epcache: %v", err)
			return nil, err
		}
	}
	return newMemcachedJob(conf, sc, epcj, stopEPCJ)
}

func newMemcachedJob(conf *MemcachedJobConfig, sc *sigmaclnt.SigmaClnt, epcj *epsrv.EPCacheJob, stopEPCJ bool) (*MemcachedJob, error) {
	var coSandbox []byte
	var coSandboxInput []byte
	var err error

	// If using init script, read boot script and prepare input
	if conf.UseCoSandbox {
		coSandbox, err = GetCoSandbox(sc)
		if err != nil {
			db.DPrintf(db.ERROR, "Err read boot script: %v", err)
			return nil, err
		}

		// Parse snapshot path to get bucket and key
		splitFN := strings.Split(conf.SnapshotPath, "/")
		bucket := splitFN[0]
		key := filepath.Join(splitFN[1:]...)

		coSandboxInput, err = GetCoSandboxInput(bucket, key, sp.LOCAL)
		if err != nil {
			db.DPrintf(db.ERROR, "Err GetCoSandboxInput: %v", err)
			return nil, err
		}
	}

	return &MemcachedJob{
		conf:            conf,
		SigmaClnt:       sc,
		EPCacheJob:      epcj,
		coSandbox:      coSandbox,
		coSandboxInput: coSandboxInput,
		stopEPCJ:        stopEPCJ,
	}, nil
}

// Start starts the memcached shim process
func (j *MemcachedJob) Start(sigmaPath string) error {
	// Create the memcached shim proc
	p := proc.NewProc("memcached-shim", []string{
		j.conf.SnapshotPath,
		strconv.Itoa(j.conf.Port),
	})
	// Add the memcached binary to be downloaded with the proc
	p.AddBin("memcached-v1.0")
	// Set MCPU
	p.SetMcpu(j.conf.Mcpu)
	// Configure proc environment
	p.GetProcEnv().UseSPProxy = j.conf.UseCoSandbox
	p.GetProcEnv().SetShmemMB(SHMEM_MB)
	p.SetCoSandbox(j.coSandbox, j.coSandboxInput)
	p.SetRunCoSandbox(j.conf.UseCoSandbox)
	// Set the proc's sigma path
	if sigmaPath != sp.NOT_SET {
		p.PrependSigmaPath(sigmaPath)
	}
	db.DPrintf(db.TEST, "Scale %v", p.GetPid())
	db.DPrintf(db.MEMCACHED, "Spawning memcached shim proc")
	err := j.Spawn(p)
	if err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err spawn: %v", err)
		return err
	}
	j.p = p
	db.DPrintf(db.MEMCACHED, "Started memcached job with pid: %v", p.GetPid())
	if err := j.WaitStart(p.GetPid()); err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err WaitStart: %v", err)
		return err
	}
	return nil
}

// Stop stops the memcached job by evicting the process
func (j *MemcachedJob) Stop() error {
	db.DPrintf(db.MEMCACHED, "Evicting memcached proc %v", j.p.GetPid())
	if err := j.Evict(j.p.GetPid()); err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err evict: %v", err)
		return err
	}
	status, err := j.WaitExit(j.p.GetPid())
	if err != nil {
		db.DPrintf(db.MEMCACHED_ERR, "Err WaitExit: %v", err)
		return err
	}
	db.DPrintf(db.MEMCACHED, "Memcached proc exited, status: %v", status)
	if !status.IsStatusEvicted() {
		db.DPrintf(db.ERROR, "Proc wrong exit status: %v", status)
		return fmt.Errorf("wrong exit status: %v", status)
	}
	j.p = nil
	if j.stopEPCJ {
		j.EPCacheJob.Stop()
	}
	return nil
}

// GetProc returns the memcached process
func (j *MemcachedJob) GetProc() *proc.Proc {
	return j.p
}

func (cfg *MemcachedJobConfig) String() string {
	return fmt.Sprintf("&{ job:%v snapshot:%v port:%v useCoSandbox:%v mcpu:%v }",
		cfg.Job, cfg.SnapshotPath, cfg.Port, cfg.UseCoSandbox, cfg.Mcpu)
}

package etcd

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
)

type EtcdJobConfig struct {
	Job           string      `json:"job"`
	SnapshotPath  string      `json:"snapshot_path"` // Path to snapshot file in SigmaOS
	Name          string      `json:"name"`          // Etcd node name
	PeerPort      int         `json:"peer_port"`
	ClientPort    int         `json:"client_port"`
	UseInitScript bool        `json:"use_init_script"`
	Mcpu          proc.Tmcpu  `json:"mcpu"`
}

func NewEtcdJobConfig(job, snapshotPath, name string, peerPort, clientPort int, useInitScript bool, mcpu proc.Tmcpu) *EtcdJobConfig {
	return &EtcdJobConfig{
		Job:           job,
		SnapshotPath:  snapshotPath,
		Name:          name,
		PeerPort:      peerPort,
		ClientPort:    clientPort,
		UseInitScript: useInitScript,
		Mcpu:          mcpu,
	}
}

type EtcdJob struct {
	mu   sync.Mutex
	conf *EtcdJobConfig
	*sigmaclnt.SigmaClnt
	p               *proc.Proc
	bootScript      []byte
	bootScriptInput []byte
}

func NewEtcdJob(conf *EtcdJobConfig, sc *sigmaclnt.SigmaClnt) (*EtcdJob, error) {
	var bootScript []byte
	var bootScriptInput []byte
	var err error

	// If using init script, read boot script and prepare input
	if conf.UseInitScript {
		bootScript, err = GetBootScript(sc)
		if err != nil {
			db.DPrintf(db.ERROR, "Err read boot script: %v", err)
			return nil, err
		}

		// Parse snapshot path to get bucket and key
		splitFN := strings.Split(conf.SnapshotPath, "/")
		bucket := splitFN[0]
		key := filepath.Join(splitFN[1:]...)

		bootScriptInput, err = GetBootScriptInput(bucket, key, sp.LOCAL)
		if err != nil {
			db.DPrintf(db.ERROR, "Err GetBootScriptInput: %v", err)
			return nil, err
		}
	}

	return &EtcdJob{
		conf:            conf,
		SigmaClnt:       sc,
		bootScript:      bootScript,
		bootScriptInput: bootScriptInput,
	}, nil
}

// Start starts the etcd shim process
func (j *EtcdJob) Start(sigmaPath string) error {
	// Create the etcd shim proc
	p := proc.NewProc("etcd-shim", []string{
		j.conf.SnapshotPath,
		j.conf.Name,
		fmt.Sprintf("http://127.0.0.1:%v", j.conf.PeerPort),
		fmt.Sprintf("http://127.0.0.1:%v", j.conf.ClientPort),
		fmt.Sprintf("http://127.0.0.1:%v", j.conf.ClientPort),
	})
	// Add the etcd binary to be downloaded with the proc
	p.AddBin("etcd-v1.0")
	// Set MCPU
	p.SetMcpu(j.conf.Mcpu)
	// Configure proc environment
	p.GetProcEnv().UseSPProxy = j.conf.UseInitScript
	p.SetBootScript(j.bootScript, j.bootScriptInput)
	p.SetRunBootScript(j.conf.UseInitScript)
	// Set the proc's sigma path
	if sigmaPath != sp.NOT_SET {
		p.PrependSigmaPath(sigmaPath)
	}
	db.DPrintf(db.TEST, "Scale %v", p.GetPid())
	db.DPrintf(db.ETCD, "Spawning etcd shim proc")
	err := j.Spawn(p)
	if err != nil {
		db.DPrintf(db.ETCD_ERR, "Err spawn: %v", err)
		return err
	}
	j.p = p
	db.DPrintf(db.ETCD, "Started etcd job with pid: %v", p.GetPid())
	if err := j.WaitStart(p.GetPid()); err != nil {
		db.DPrintf(db.ETCD_ERR, "Err WaitStart: %v", err)
		return err
	}
	return nil
}

// Stop stops the etcd job by evicting the process
func (j *EtcdJob) Stop() error {
	db.DPrintf(db.ETCD, "Evicting etcd proc %v", j.p.GetPid())
	if err := j.Evict(j.p.GetPid()); err != nil {
		db.DPrintf(db.ETCD_ERR, "Err evict: %v", err)
		return err
	}
	status, err := j.WaitExit(j.p.GetPid())
	if err != nil {
		db.DPrintf(db.ETCD_ERR, "Err WaitExit: %v", err)
		return err
	}
	db.DPrintf(db.ETCD, "Etcd proc exited, status: %v", status)
	if !status.IsStatusEvicted() {
		db.DPrintf(db.ERROR, "Proc wrong exit status: %v", status)
		return fmt.Errorf("wrong exit status: %v", status)
	}
	j.p = nil
	return nil
}

// GetProc returns the etcd process
func (j *EtcdJob) GetProc() *proc.Proc {
	return j.p
}

func (cfg *EtcdJobConfig) String() string {
	return fmt.Sprintf("&{ job:%v snapshot:%v name:%v peerPort:%v clientPort:%v useInitScript:%v mcpu:%v }",
		cfg.Job, cfg.SnapshotPath, cfg.Name, cfg.PeerPort, cfg.ClientPort, cfg.UseInitScript, cfg.Mcpu)
}

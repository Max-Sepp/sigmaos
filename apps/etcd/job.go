package etcd

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	epsrv "sigmaos/apps/epcache/srv"
	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
)

const SHMEM_MB proc.Tmem = 250

type EtcdJobConfig struct {
	Job            string     `json:"job"`
	SnapshotS3Path string     `json:"snapshot_s3_path"` // Path to snapshot in S3 (name/s3/~local/...)
	SnapshotUXPath string     `json:"snapshot_ux_path"` // Path to snapshot in UX (name/ux/~local/...)
	UseUX          bool       `json:"use_ux"`           // Use UX path instead of S3 path
	Name           string     `json:"name"`             // Etcd node name
	PeerPort       int        `json:"peer_port"`
	ClientPort     int        `json:"client_port"`
	UseCoSandbox   bool       `json:"use_co_sandbox"`
	Mcpu           proc.Tmcpu `json:"mcpu"`
}

func NewEtcdJobConfig(job, snapshotS3Path, snapshotUXPath, name string, peerPort, clientPort int, useCoSandbox, useUX bool, mcpu proc.Tmcpu) *EtcdJobConfig {
	return &EtcdJobConfig{
		Job:            job,
		SnapshotS3Path: snapshotS3Path,
		SnapshotUXPath: snapshotUXPath,
		UseUX:          useUX,
		Name:           name,
		PeerPort:       peerPort,
		ClientPort:     clientPort,
		UseCoSandbox:   useCoSandbox,
		Mcpu:           mcpu,
	}
}

type EtcdJob struct {
	mu   sync.Mutex
	conf *EtcdJobConfig
	*sigmaclnt.SigmaClnt
	EPCacheJob     *epsrv.EPCacheJob
	p              *proc.Proc
	coSandbox      []byte
	coSandboxInput []byte
	stopEPCJ       bool
}

func NewEtcdJob(conf *EtcdJobConfig, sc *sigmaclnt.SigmaClnt, epcj *epsrv.EPCacheJob) (*EtcdJob, error) {
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
	return newEtcdJob(conf, sc, epcj, stopEPCJ)
}

func newEtcdJob(conf *EtcdJobConfig, sc *sigmaclnt.SigmaClnt, epcj *epsrv.EPCacheJob, stopEPCJ bool) (*EtcdJob, error) {
	var coSandbox []byte
	var coSandboxInput []byte
	var err error

	// If using init script, read boot script and prepare input
	if conf.UseCoSandbox {
		if conf.UseUX {
			coSandbox, err = GetCoSandboxUX(sc)
			if err != nil {
				db.DPrintf(db.ERROR, "Err read ux boot script: %v", err)
				return nil, err
			}
			strippedUX := strings.TrimPrefix(conf.SnapshotUXPath, sp.UX+sp.LOCAL+"/")
			coSandboxInput, err = GetCoSandboxUXInput(strippedUX, sp.LOCAL)
			if err != nil {
				db.DPrintf(db.ERROR, "Err GetCoSandboxUXInput: %v", err)
				return nil, err
			}
		} else {
			coSandbox, err = GetCoSandbox(sc)
			if err != nil {
				db.DPrintf(db.ERROR, "Err read boot script: %v", err)
				return nil, err
			}
			// Parse S3 snapshot path to get bucket and key (strip name/s3/~local/ prefix)
			strippedS3 := strings.TrimPrefix(conf.SnapshotS3Path, sp.S3+sp.LOCAL+"/")
			splitFN := strings.Split(strippedS3, "/")
			bucket := splitFN[0]
			key := filepath.Join(splitFN[1:]...)
			coSandboxInput, err = GetCoSandboxInput(bucket, key, sp.LOCAL)
			if err != nil {
				db.DPrintf(db.ERROR, "Err GetCoSandboxInput: %v", err)
				return nil, err
			}
		}
	}

	return &EtcdJob{
		conf:           conf,
		SigmaClnt:      sc,
		EPCacheJob:     epcj,
		coSandbox:      coSandbox,
		coSandboxInput: coSandboxInput,
		stopEPCJ:       stopEPCJ,
	}, nil
}

// Start starts the etcd shim process
func (j *EtcdJob) Start(sigmaPath string) error {
	// Create the etcd shim proc
	snapPn := j.conf.SnapshotS3Path
	if j.conf.UseUX {
		snapPn = j.conf.SnapshotUXPath
	}
	p := proc.NewProc("etcd-shim", []string{
		snapPn,
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
	p.GetProcEnv().UseSPProxy = true
	p.SetShmemMB(SHMEM_MB)
	p.SetCoSandbox(j.coSandbox, j.coSandboxInput)
	p.SetRunCoSandbox(j.conf.UseCoSandbox)
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
	if j.stopEPCJ {
		j.EPCacheJob.Stop()
	}
	return nil
}

// GetProc returns the etcd process
func (j *EtcdJob) GetProc() *proc.Proc {
	return j.p
}

func (cfg *EtcdJobConfig) String() string {
	return fmt.Sprintf("&{ job:%v snapshotS3:%v snapshotUX:%v useUX:%v name:%v peerPort:%v clientPort:%v useCoSandbox:%v mcpu:%v }",
		cfg.Job, cfg.SnapshotS3Path, cfg.SnapshotUXPath, cfg.UseUX, cfg.Name, cfg.PeerPort, cfg.ClientPort, cfg.UseCoSandbox, cfg.Mcpu)
}

// DownloadSnapToAllUXs copies the snapshot from name/s3/~any/<srcPath> to
// name/ux/<child>/<dstPath> for every child of name/ux/.
func (j *EtcdJob) DownloadSnapToAllUXs(srcPath, dstPath string) error {
	sts, err := j.GetDir(sp.UX)
	if err != nil {
		db.DPrintf(db.ERROR, "DownloadSnapToAllUXs: GetDir %v: %v", sp.UX, err)
		return err
	}
	for _, st := range sts {
		uxBase := filepath.Join(sp.UX, st.Name)
		src := filepath.Join(sp.S3+sp.ANY, srcPath)
		dst := filepath.Join(uxBase, dstPath)
		if err := j.CopyFile(src, dst); err != nil {
			db.DPrintf(db.ERROR, "DownloadSnapToAllUXs: CopyFile %v -> %v: %v", src, dst, err)
			return err
		}
	}
	return nil
}

package imgrec_py

import (
	"fmt"

	db "sigmaos/debug"
	"sigmaos/proc"
	wasmrt "sigmaos/proxy/wasm/rpc/wasmer"
	"sigmaos/sigmaclnt"
)

const cosandboxName = "imgrec_boot"

type ImgrecPyJobConfig struct {
	ImgBucket    string    `json:"img_bucket"`
	ImgKey       string    `json:"img_key"`
	ModelBucket  string    `json:"model_bucket"`
	ModelKey     string    `json:"model_key"`
	Kid          string    `json:"kid"`
	UseCoSandbox bool      `json:"use_co_sandbox"`
	ShmemMB      proc.Tmem `json:"shmem_mb"`
}

func NewImgrecPyJobConfig(imgBucket, imgKey, modelBucket, modelKey, kid string, useCoSandbox bool, shmemMB proc.Tmem) *ImgrecPyJobConfig {
	return &ImgrecPyJobConfig{
		ImgBucket:    imgBucket,
		ImgKey:       imgKey,
		ModelBucket:  modelBucket,
		ModelKey:     modelKey,
		Kid:          kid,
		UseCoSandbox: useCoSandbox,
		ShmemMB:      shmemMB,
	}
}

type ImgrecPyJob struct {
	conf *ImgrecPyJobConfig
	*sigmaclnt.SigmaClnt
	coSandbox []byte
	bootInput []byte
}

func NewImgrecPyJob(conf *ImgrecPyJobConfig, sc *sigmaclnt.SigmaClnt) (*ImgrecPyJob, error) {
	j := &ImgrecPyJob{conf: conf, SigmaClnt: sc}
	if conf.UseCoSandbox {
		b, err := wasmrt.ReadCoSandbox(sc, cosandboxName)
		if err != nil {
			db.DPrintf(db.ERROR, "ImgrecPy ReadCoSandbox err: %v", err)
			return nil, err
		}
		j.coSandbox = b
		j.bootInput = wasmrt.EncodeArgs([]string{conf.ImgBucket, conf.ImgKey, conf.ModelBucket, conf.ModelKey, conf.Kid})
	}
	return j, nil
}

// Run spawns an imgrec.py proc, waits for it to complete, and returns the
// result message (class_idx,score).
func (j *ImgrecPyJob) Run() (string, error) {
	p := proc.NewProc("imgrec.py", []string{
		j.conf.ImgBucket, j.conf.ImgKey,
		j.conf.ModelBucket, j.conf.ModelKey,
		j.conf.Kid,
	})
	p.GetProcEnv().UseSPProxy = true
	p.GetProcEnv().UseSPProxyProcClnt = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_PYTHON)
	if j.conf.UseCoSandbox {
		p.SetCoSandbox(j.coSandbox, j.bootInput)
		p.SetRunCoSandbox(true)
	}
	if j.conf.ShmemMB > 0 {
		p.SetShmemMB(j.conf.ShmemMB)
	}
	db.DPrintf(db.TEST, "Scale %v", p.GetPid())
	if err := j.Spawn(p); err != nil {
		db.DPrintf(db.ERROR, "ImgrecPy Spawn err: %v", err)
		return "", err
	}
	if err := j.WaitStart(p.GetPid()); err != nil {
		db.DPrintf(db.ERROR, "ImgrecPy WaitStart err: %v", err)
		return "", err
	}
	status, err := j.WaitExit(p.GetPid())
	if err != nil {
		db.DPrintf(db.ERROR, "ImgrecPy WaitExit err: %v", err)
		return "", err
	}
	if !status.IsStatusOK() {
		return "", fmt.Errorf("imgrec.py exited with status: %v", status)
	}
	return status.Msg(), nil
}

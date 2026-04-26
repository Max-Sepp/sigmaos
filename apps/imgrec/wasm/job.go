package imgrec_wasm

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	db "sigmaos/debug"
	"sigmaos/proc"
	wasmrt "sigmaos/proxy/wasm/rpc/wasmer"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
)

const (
	PrecompiledProg = "imgrec_precompiled.wasm"
	cosandboxName   = "imgrec_boot"
)

type ImgrecWASMJobConfig struct {
	ImgBucket    string    `json:"img_bucket"`
	ImgKey       string    `json:"img_key"`
	ModelBucket  string    `json:"model_bucket"`
	ModelKey     string    `json:"model_key"`
	Kid          string    `json:"kid"`
	UseDelegated bool      `json:"use_delegated"`
	UseCoSandbox bool      `json:"use_co_sandbox"`
	ShmemMB      proc.Tmem `json:"shmem_mb"`
}

func NewImgrecWASMJobConfig(imgBucket, imgKey, modelBucket, modelKey, kid string, useDelegated, useCoSandbox bool, shmemMB proc.Tmem) *ImgrecWASMJobConfig {
	return &ImgrecWASMJobConfig{
		ImgBucket:    imgBucket,
		ImgKey:       imgKey,
		ModelBucket:  modelBucket,
		ModelKey:     modelKey,
		Kid:          kid,
		UseDelegated: useDelegated,
		UseCoSandbox: useCoSandbox,
		ShmemMB:      shmemMB,
	}
}

type ImgrecWASMJob struct {
	conf             *ImgrecWASMJobConfig
	*sigmaclnt.SigmaClnt
	coSandbox        []byte
	bootInput        []byte
	precompiledBytes []byte
}

// NewImgrecWASMJob creates a new job, reading and precompiling the WASM binary
// and (if UseCoSandbox) fetching the cosandbox boot script.
func NewImgrecWASMJob(conf *ImgrecWASMJobConfig, sc *sigmaclnt.SigmaClnt) (*ImgrecWASMJob, error) {
	j := &ImgrecWASMJob{conf: conf, SigmaClnt: sc}
	if conf.UseCoSandbox {
		b, err := wasmrt.ReadCoSandbox(sc, cosandboxName)
		if err != nil {
			db.DPrintf(db.ERROR, "ImgrecWASM ReadCoSandbox err: %v", err)
			return nil, err
		}
		j.coSandbox = b
		j.bootInput = wasmrt.EncodeArgs([]string{conf.ImgBucket, conf.ImgKey, conf.ModelBucket, conf.ModelKey, conf.Kid})
	}
	raw, err := os.ReadFile(localWASMBinPath())
	if err != nil {
		db.DPrintf(db.ERROR, "ImgrecWASM ReadFile wasm bin err: %v", err)
		return nil, err
	}
	wrt := wasmrt.NewWasmerRuntime(nil)
	compiled, err := wrt.PrecompileModule(raw)
	if err != nil {
		db.DPrintf(db.ERROR, "ImgrecWASM PrecompileModule err: %v", err)
		return nil, err
	}
	j.precompiledBytes = compiled
	return j, nil
}

// Run uploads the precompiled WASM binary, spawns the proc, waits for
// completion, and returns the result message (class_idx,score).
func (j *ImgrecWASMJob) Run() (string, error) {
	precompiledPath := precompiledBinPath(j.SigmaClnt)
	j.Remove(precompiledPath)
	wrt, err := j.CreateWriter(precompiledPath, 0777, sp.OWRITE)
	if err != nil {
		db.DPrintf(db.ERROR, "ImgrecWASM CreateWriter err: %v", err)
		return "", err
	}
	if _, err := io.Copy(wrt, bytes.NewReader(j.precompiledBytes)); err != nil {
		wrt.Close()
		db.DPrintf(db.ERROR, "ImgrecWASM upload precompiled wasm err: %v", err)
		return "", err
	}
	wrt.Close()

	useDelegatedStr := "false"
	if j.conf.UseDelegated {
		useDelegatedStr = "true"
	}
	p := proc.NewProc(PrecompiledProg, []string{
		j.conf.ImgBucket, j.conf.ImgKey,
		j.conf.ModelBucket, j.conf.ModelKey,
		j.conf.Kid, useDelegatedStr,
	})
	p.GetProcEnv().UseSPProxy = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_WASM)
	if j.conf.UseCoSandbox {
		p.SetCoSandbox(j.coSandbox, j.bootInput)
		p.SetRunCoSandbox(true)
	}
	if j.conf.ShmemMB > 0 {
		p.SetShmemMB(j.conf.ShmemMB)
	}
	if j.ProcEnv().BuildTag == sp.LOCAL_BUILD {
		p.PrependSigmaPath(filepath.Dir(precompiledPath))
	}
	if err := j.Spawn(p); err != nil {
		db.DPrintf(db.ERROR, "ImgrecWASM Spawn err: %v", err)
		return "", err
	}
	if err := j.WaitStart(p.GetPid()); err != nil {
		db.DPrintf(db.ERROR, "ImgrecWASM WaitStart err: %v", err)
		return "", err
	}
	status, err := j.WaitExit(p.GetPid())
	if err != nil {
		db.DPrintf(db.ERROR, "ImgrecWASM WaitExit err: %v", err)
		return "", err
	}
	if !status.IsStatusOK() {
		return "", fmt.Errorf("imgrec_precompiled.wasm exited with status: %v", status)
	}
	return status.Msg(), nil
}

// localWASMBinPath returns the path to the raw imgrec.wasm binary on the local
// filesystem, derived from this source file's location.
func localWASMBinPath() string {
	_, b, _, _ := runtime.Caller(0)
	// b is .../apps/imgrec/wasm/job.go; go up 4 levels to project root
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(b))))
	return filepath.Join(projectRoot, "bin/wasm/imgrec.wasm")
}

func precompiledBinPath(sc *sigmaclnt.SigmaClnt) sp.Tsigmapath {
	if sc.ProcEnv().BuildTag == sp.LOCAL_BUILD {
		return filepath.Join(sp.S3, sp.ANY, "9ps3", PrecompiledProg+"-v"+sp.Version)
	}
	return filepath.Join(sp.S3, sp.ANY, sc.ProcEnv().BuildTag, PrecompiledProg)
}

package sebs

import (
	"encoding/json"
	"fmt"
	"path"

	db "sigmaos/debug"
	"sigmaos/proc"
	wasmrt "sigmaos/proxy/wasm/rpc/wasmer"
	"sigmaos/sigmaclnt"
)

// S3Object is a (bucket, key) pair identifying an S3 object to pre-fetch.
type S3Object struct {
	Bucket string
	Key    string
}

// SebsWASMJobConfig extends SebsJobConfig with cosandbox pre-fetch configuration.
// BootObjects is ordered by rpcIdx: BootObjects[i] will be pre-fetched at rpcIdx=i
// by the cosandbox boot script.
type SebsWASMJobConfig struct {
	SebsJobConfig
	CoSandboxName string
	BootObjects   []S3Object
	Kid           string
}

func newSebsWASMJobConfig(benchmark, event, cosandboxName, kid string, bootObjs []S3Object, mcpu proc.Tmcpu, shmemMB proc.Tmem) *SebsWASMJobConfig {
	base := NewSebsJobConfig(benchmark, event, false, true, false, shmemMB, mcpu)
	return &SebsWASMJobConfig{
		SebsJobConfig: *base,
		CoSandboxName: cosandboxName,
		BootObjects:   bootObjs,
		Kid:           kid,
	}
}

// sebsSingleS3Event is the event shape shared by thumbnailer, video-processing,
// and dna-visualisation (all download one object from bucket.input/object.key).
type sebsSingleS3Event struct {
	Bucket struct {
		Bucket string `json:"bucket"`
		Input  string `json:"input"`
	} `json:"bucket"`
	Object struct {
		Key string `json:"key"`
	} `json:"object"`
}

func NewSebsThumbnailerWASMJobConfig(event, kid string, mcpu proc.Tmcpu, shmemMB proc.Tmem) (*SebsWASMJobConfig, error) {
	var ev sebsSingleS3Event
	if err := json.Unmarshal([]byte(event), &ev); err != nil {
		return nil, fmt.Errorf("NewSebsThumbnailerWASMJobConfig: %w", err)
	}
	objs := []S3Object{{ev.Bucket.Bucket, path.Join(ev.Bucket.Input, ev.Object.Key)}}
	return newSebsWASMJobConfig("210.thumbnailer", event, "s3get_boot", kid, objs, mcpu, shmemMB), nil
}

func NewSebsVideoProcessingWASMJobConfig(event, kid string, mcpu proc.Tmcpu, shmemMB proc.Tmem) (*SebsWASMJobConfig, error) {
	var ev sebsSingleS3Event
	if err := json.Unmarshal([]byte(event), &ev); err != nil {
		return nil, fmt.Errorf("NewSebsVideoProcessingWASMJobConfig: %w", err)
	}
	objs := []S3Object{{ev.Bucket.Bucket, path.Join(ev.Bucket.Input, ev.Object.Key)}}
	return newSebsWASMJobConfig("220.video-processing", event, "s3get_boot", kid, objs, mcpu, shmemMB), nil
}

func NewSebsDnaVisualisationWASMJobConfig(event, kid string, mcpu proc.Tmcpu, shmemMB proc.Tmem) (*SebsWASMJobConfig, error) {
	var ev sebsSingleS3Event
	if err := json.Unmarshal([]byte(event), &ev); err != nil {
		return nil, fmt.Errorf("NewSebsDnaVisualisationWASMJobConfig: %w", err)
	}
	objs := []S3Object{{ev.Bucket.Bucket, path.Join(ev.Bucket.Input, ev.Object.Key)}}
	return newSebsWASMJobConfig("504.dna-visualisation", event, "s3get_boot", kid, objs, mcpu, shmemMB), nil
}

func NewSebsImageRecognitionWASMJobConfig(event, kid string, mcpu proc.Tmcpu, shmemMB proc.Tmem) (*SebsWASMJobConfig, error) {
	var ev struct {
		Bucket struct {
			Bucket string `json:"bucket"`
			Model  string `json:"model"`
			Input  string `json:"input"`
		} `json:"bucket"`
		Object struct {
			Model string `json:"model"`
			Input string `json:"input"`
		} `json:"object"`
	}
	if err := json.Unmarshal([]byte(event), &ev); err != nil {
		return nil, fmt.Errorf("NewSebsImageRecognitionWASMJobConfig: %w", err)
	}
	// imgrec_boot sends model at rpcIdx=0, image at rpcIdx=1.
	objs := []S3Object{
		{ev.Bucket.Bucket, path.Join(ev.Bucket.Model, ev.Object.Model)}, // rpcIdx=0
		{ev.Bucket.Bucket, path.Join(ev.Bucket.Input, ev.Object.Input)}, // rpcIdx=1
	}
	return newSebsWASMJobConfig("411.image-recognition", event, "imgrec_boot", kid, objs, mcpu, shmemMB), nil
}

type SebsWASMJob struct {
	conf            *SebsWASMJobConfig
	precompiledWASM []byte
	*sigmaclnt.SigmaClnt
}

func NewSebsWASMJob(conf *SebsWASMJobConfig, sc *sigmaclnt.SigmaClnt) (*SebsWASMJob, error) {
	b, err := wasmrt.ReadCoSandbox(sc, conf.CoSandboxName)
	if err != nil {
		db.DPrintf(db.ERROR, "SebsWASM ReadCoSandbox(%v) err: %v", conf.CoSandboxName, err)
		return nil, err
	}
	return &SebsWASMJob{conf: conf, SigmaClnt: sc, precompiledWASM: b}, nil
}

func (j *SebsWASMJob) Run() (string, error) {
	bootInput := j.buildBootInput()
	delegatedMap, err := j.buildDelegatedMap()
	if err != nil {
		return "", err
	}

	args := []string{"--benchmark", j.conf.Benchmark, "--event", j.conf.Event, "--delegated"}
	p := proc.NewProc("sebs-runner.py", args)
	p.AddBin(fmt.Sprintf("%v-bundle.tar.gz", j.conf.Benchmark))
	p.GetProcEnv().UseSPProxy = true
	p.GetProcEnv().UseSPProxyProcClnt = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_PYTHON)
	p.SetCoSandbox(j.precompiledWASM, bootInput)
	p.SetRunCoSandbox(true)
	p.AppendEnv("SEBS_DELEGATED_MAP", delegatedMap)
	if j.conf.Mcpu > 0 {
		p.SetMcpu(j.conf.Mcpu)
	}
	if j.conf.ShmemMB > 0 {
		p.SetShmemMB(j.conf.ShmemMB)
	}
	db.DPrintf(db.TEST, "Scale %v", p.GetPid())
	db.DPrintf(db.TEST, "SebsWASMJob %v %v", j.conf.Benchmark, p.GetPid())
	if err := j.Spawn(p); err != nil {
		db.DPrintf(db.ERROR, "SebsWASMJob Spawn err: %v", err)
		return "", err
	}
	if err := j.WaitStart(p.GetPid()); err != nil {
		db.DPrintf(db.ERROR, "SebsWASMJob WaitStart err: %v", err)
		return "", err
	}
	status, err := j.WaitExit(p.GetPid())
	if err != nil {
		db.DPrintf(db.ERROR, "SebsWASMJob WaitExit err: %v", err)
		return "", err
	}
	if !status.IsStatusOK() {
		return "", fmt.Errorf("sebs-runner.py [%v] exited with status: %v", j.conf.Benchmark, status)
	}
	return status.Msg(), nil
}

// buildBootInput encodes the cosandbox boot script input. The encoding is
// per-crate: each crate defines its own expected input buffer layout.
func (j *SebsWASMJob) buildBootInput() []byte {
	switch j.conf.CoSandboxName {
	case "s3get_boot":
		// s3get_boot input: bucket, key, kid (3 strings via EncodeArgs)
		obj := j.conf.BootObjects[0]
		return wasmrt.EncodeArgs([]string{obj.Bucket, obj.Key, j.conf.Kid})
	case "imgrec_boot":
		// imgrec_boot input layout: img_bucket, img_key, model_bucket, model_key, kid.
		// BootObjects[0] = model (rpcIdx=0), BootObjects[1] = image (rpcIdx=1).
		// The buffer layout puts image first despite model having the lower rpcIdx.
		model := j.conf.BootObjects[0]
		img := j.conf.BootObjects[1]
		return wasmrt.EncodeArgs([]string{img.Bucket, img.Key, model.Bucket, model.Key, j.conf.Kid})
	default:
		db.DFatalf("SebsWASMJob: unknown cosandbox: %v", j.conf.CoSandboxName)
		return nil
	}
}

// buildDelegatedMap returns the SEBS_DELEGATED_MAP JSON string.
// Format: [[bucket0, key0, 0], [bucket1, key1, 1], ...]
// where the integer is the rpcIdx the cosandbox used for that object.
func (j *SebsWASMJob) buildDelegatedMap() (string, error) {
	entries := make([][]interface{}, len(j.conf.BootObjects))
	for i, obj := range j.conf.BootObjects {
		entries[i] = []interface{}{obj.Bucket, obj.Key, i}
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("SebsWASMJob buildDelegatedMap: %w", err)
	}
	return string(b), nil
}

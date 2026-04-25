package imgresize

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	db "sigmaos/debug"
	"sigmaos/ft/procgroupmgr"
	"sigmaos/ft/task"
	fttask_clnt "sigmaos/ft/task/clnt"
	fttask_mgr "sigmaos/ft/task/fttaskmgr"
	fttask_srv "sigmaos/ft/task/srv"
	"sigmaos/proc"
	"sigmaos/proxy/wasm/rpc/wasmer"
	"sigmaos/serr"
	"sigmaos/sigmaclnt"
	"sigmaos/sigmaclnt/fslib"
	sp "sigmaos/sigmap"
	"sigmaos/util/crash"
	rd "sigmaos/util/rand"
)

const SHMEM_MB proc.Tmem = 10

const (
	TASKSVC = "imgresize-tasksvc"
	IMGSVC  = "imgresize"
)

type ImgdJobConfig struct {
	Job                   string     `json:"job"`
	WorkerMcpu            proc.Tmcpu `json:"worker_mcpu"`
	WorkerMem             proc.Tmem  `json:"worker_mem"`
	Persist               bool       `json:"persist"`
	NRounds               int        `json:"n_rounds"`
	ImgdMcpu              proc.Tmcpu `json:"imgd_mcpu"`
	UseSPProxy            bool       `json:"use_sp_proxy"`
	UseCoSandbox         bool       `json:"use_co_sandbox"`
	WriteOutViaCoSandbox bool       `json:"write_out_via_co_sandbox"`
	UseS3Clnt             bool       `json:"use_s3_clnt"`
	WorkerCoSandboxMcpu  proc.Tmcpu `json:"worker_co_sandbox_mcpu"`
	WorkerCoSandboxMem   proc.Tmem  `json:"worker_co_sandbox_mem"`
	FTTaskSrvMcpu         proc.Tmcpu `json:"ft_task_srv_mcpu"`
	ImgDim                int        `json:"img_dim"`
	PremountS3            bool       `json:"premount_s3"`
	MeasurePSS            bool       `json:"measure_pss"`
	BailOut               bool       `json:"bail_out"`
}

func NewImgdJobConfig(job string, workerMcpu proc.Tmcpu, workerMem proc.Tmem, persist bool, nrounds int, imgdMcpu proc.Tmcpu, useSPProxy bool, useCoSandbox bool, useS3Clnt bool, workerCoSandboxMcpu proc.Tmcpu, workerCoSandboxMem proc.Tmem, ftTaskSrvMcpu proc.Tmcpu) *ImgdJobConfig {
	return &ImgdJobConfig{
		Job:                  job,
		WorkerMcpu:           workerMcpu,
		WorkerMem:            workerMem,
		Persist:              persist,
		NRounds:              nrounds,
		ImgdMcpu:             imgdMcpu,
		UseSPProxy:           useSPProxy,
		UseCoSandbox:        useCoSandbox,
		UseS3Clnt:            useS3Clnt,
		WorkerCoSandboxMcpu: workerCoSandboxMcpu,
		WorkerCoSandboxMem:  workerCoSandboxMem,
		FTTaskSrvMcpu:        ftTaskSrvMcpu,
	}
}

func (cfg *ImgdJobConfig) String() string {
	return fmt.Sprintf("&{ job:%v workerMcpu:%v workerMem:%v persist:%v nrounds:%v imgdMcpu:%v useSPProxy:%v useCoSandbox:%v useS3Clnt:%v workerCoSandboxMcpu:%v workerCoSandboxMem:%v ftTaskSrvMcpu:%v imgDim:%v premountS3:%v measurePSS:%v bailOut:%v }", cfg.Job, cfg.WorkerMcpu, cfg.WorkerMem, cfg.Persist, cfg.NRounds, cfg.ImgdMcpu, cfg.UseSPProxy, cfg.UseCoSandbox, cfg.UseS3Clnt, cfg.WorkerCoSandboxMcpu, cfg.WorkerCoSandboxMem, cfg.FTTaskSrvMcpu, cfg.ImgDim, cfg.PremountS3, cfg.MeasurePSS, cfg.BailOut)
}

func GetCoSandboxBailOut(sc *sigmaclnt.SigmaClnt) ([]byte, error) {
	return wasmer.ReadCoSandbox(sc, "imgprocess_bail_out_boot")
}

func GetCoSandboxWriteOut(sc *sigmaclnt.SigmaClnt) ([]byte, error) {
	return wasmer.ReadCoSandbox(sc, "imgprocess_write_out_boot")
}

func GetCoSandbox(sc *sigmaclnt.SigmaClnt) ([]byte, error) {
	return wasmer.ReadCoSandbox(sc, "s3get_boot")
}

func GetCoSandboxInput(bucket, key, kid string) ([]byte, error) {
	return wasmer.EncodeArgs([]string{bucket, key, kid}), nil
}

func ImgSvcId(job string) string {
	return fmt.Sprintf("%s-%s", IMGSVC, job)
}

func TaskSvcId(job string) string {
	return fmt.Sprintf("%s-%s", TASKSVC, job)
}

type Ttask struct {
	FileName              string `json:"file"`
	UseS3Clnt             bool   `json:"use_s3_clnt"`
	UseCoSandbox         bool   `json:"use_co_sandbox"`
	WriteOutViaCoSandbox bool   `json:"write_out_via_co_sandbox"`
}

func NewTask(fn string, useS3Clnt, useCoSandbox, writeOutViaCoSandbox bool) *Ttask {
	return &Ttask{
		FileName:              fn,
		UseS3Clnt:             useS3Clnt,
		UseCoSandbox:         useCoSandbox,
		WriteOutViaCoSandbox: writeOutViaCoSandbox,
	}
}

type ImgdMgr[Data any] struct {
	job    string
	jobCfg *ImgdJobConfig
	pgm    *procgroupmgr.ProcGroupMgr
	ftsrv  *fttask_srv.FtTaskSrvMgr
}

func NewImgdMgr[Data any](sc *sigmaclnt.SigmaClnt, jobCfg *ImgdJobConfig, em *crash.TeventMap) (*ImgdMgr[Data], error) {
	crash.SetSigmaFail(em)
	imgd := &ImgdMgr[Data]{}

	imgd.job = jobCfg.Job
	imgd.jobCfg = jobCfg
	var err error
	imgd.ftsrv, err = fttask_srv.NewFtTaskSrvMgr(sc, TaskSvcId(jobCfg.Job), false, imgd.jobCfg.FTTaskSrvMcpu)
	if err != nil {
		return nil, err
	}

	if err := sc.MkDir(sp.IMG, 0777); err != nil && !serr.IsErrorExists(err) {
		return nil, err
	}

	cfg := procgroupmgr.NewProcGroupConfig(1, "imgresized", []string{strconv.Itoa(int(jobCfg.WorkerMcpu)), strconv.Itoa(int(jobCfg.WorkerMem)), strconv.Itoa(jobCfg.NRounds), TaskSvcId(imgd.job), strconv.FormatBool(jobCfg.UseSPProxy), strconv.FormatBool(jobCfg.UseCoSandbox), strconv.Itoa(int(jobCfg.WorkerCoSandboxMcpu)), strconv.Itoa(int(jobCfg.WorkerCoSandboxMem)), strconv.FormatBool(jobCfg.WriteOutViaCoSandbox), strconv.Itoa(jobCfg.ImgDim), strconv.FormatBool(jobCfg.PremountS3), strconv.FormatBool(jobCfg.MeasurePSS), strconv.FormatBool(jobCfg.BailOut)}, jobCfg.ImgdMcpu, ImgSvcId(jobCfg.Job))

	if jobCfg.Persist {
		cfg.Persist(sc.FsLib)
	}
	imgd.pgm = cfg.StartGrpMgr(sc)
	return imgd, nil
}

func (imgd *ImgdMgr[Data]) NewImgdClnt(sc *sigmaclnt.SigmaClnt) (*ImgdClnt[Data], error) {
	clnt, err := NewImgdClnt[Data](sc, imgd.job, imgd.ftsrv.Id, sp.NullFence())
	if err != nil {
		return nil, err
	}
	if err := clnt.SetImgdFence(); err != nil {
		return nil, err
	}
	return clnt, nil
}

func (imgd *ImgdMgr[Data]) Restart(sc *sigmaclnt.SigmaClnt) error {
	var err error
	imgd.ftsrv, err = fttask_srv.NewFtTaskSrvMgr(sc, TaskSvcId(imgd.job), false, imgd.jobCfg.FTTaskSrvMcpu)
	if err != nil {
		return err
	}

	cfgs, err := procgroupmgr.Recover(sc)
	if err != nil {
		return err
	}
	if len(cfgs) < 1 {
		return fmt.Errorf("Too few procgroup cfgs")
	}
	imgd.pgm = cfgs[0].StartGrpMgr(sc)
	return nil
}

func (imgd *ImgdMgr[Data]) WaitImgd() []*procgroupmgr.ProcStatus {
	sts := imgd.pgm.WaitGroup()
	imgd.ftsrv.Stop(true)
	return sts
}

func (imgd *ImgdMgr[Data]) StopImgd(clearStore bool) ([]*procgroupmgr.ProcStatus, error) {
	sts, err := imgd.pgm.StopGroup()
	imgd.ftsrv.Stop(clearStore)
	return sts, err
}

// remove old thumbnails
func Cleanup(fsl *fslib.FsLib, dir string) error {
	_, err := fsl.ProcessDir(dir, func(st *sp.Tstat) (bool, error) {
		if IsThumbNail(st.Name) {
			err := fsl.Remove(filepath.Join(dir, st.Name))
			if err != nil {
				return true, err
			}
			return false, nil
		}
		return false, nil
	})
	return err
}

func ThumbName(fn string) string {
	ext := filepath.Ext(fn)
	fn1 := strings.TrimSuffix(fn, ext) + "-" + rd.String(4) + "-thumb" + filepath.Ext(fn)
	return fn1
}

func IsThumbNail(fn string) bool {
	return strings.Contains(fn, "-thumb")
}

func GetMkProcFn(serverId task.FtTaskSvcId, nrounds int, imgDim int, workerMcpu proc.Tmcpu, workerMem proc.Tmem, workerCoSandboxMcpu proc.Tmcpu, workerCoSandboxMem proc.Tmem, coSandbox []byte, coSandboxWriteOut []byte, useSPProxy bool, premountS3 bool, s3EP *sp.Tendpoint, measurePSS bool, bailOut bool) fttask_mgr.TnewProc[Ttask] {
	return func(task fttask_clnt.Task[Ttask]) (*proc.Proc, error) {
		db.DPrintf(db.IMGD, "mkProc %v", task)
		fn := task.Data.FileName
		p := proc.NewProcPid(sp.GenPid(string(serverId)), "imgresize", []string{fn, ThumbName(fn), strconv.Itoa(nrounds), strconv.FormatBool(task.Data.UseS3Clnt), strconv.FormatBool(task.Data.WriteOutViaCoSandbox), strconv.Itoa(imgDim), strconv.FormatBool(bailOut)})
		p.SetMcpu(workerMcpu)
		p.SetMem(workerMem)
		p.SetShmemMB(SHMEM_MB)
		p.SetMeasurePSS(measurePSS, 100)
		if premountS3 {
			p.SetCachedEndpoint(filepath.Join(sp.S3, sp.LOCAL), s3EP)
		}
		splitFN := strings.Split(fn, "/")
		coSandboxInput, err := GetCoSandboxInput(splitFN[0], filepath.Join(splitFN[1:]...), sp.LOCAL)
		if err != nil {
			db.DPrintf(db.ERROR, "Err get cosandbox input")
			return nil, err
		}
		p.GetProcEnv().UseSPProxy = useSPProxy
		if task.Data.WriteOutViaCoSandbox {
			p.SetCoSandbox(coSandboxWriteOut, coSandboxInput)
		} else {
			p.SetCoSandbox(coSandbox, coSandboxInput)
		}
		p.SetRunCoSandbox(task.Data.UseCoSandbox)
		if task.Data.UseCoSandbox {
			// Run after boot script, if we set a resource reservation for the boot
			// script
			if workerCoSandboxMcpu > 0 || workerCoSandboxMem > 0 {
				p.SetRunAfterCoSandbox(true)
			}
			p.SetCoSandboxMcpu(workerCoSandboxMcpu)
			p.SetCoSandboxMem(workerCoSandboxMem)
		}
		return p, nil
	}
}

package imgresize

import (
	"bytes"
	"encoding/binary"
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
	UseBootScript         bool       `json:"use_boot_script"`
	WriteOutViaBootScript bool       `json:"write_out_via_boot_script"`
	UseS3Clnt             bool       `json:"use_s3_clnt"`
	WorkerBootScriptMcpu  proc.Tmcpu `json:"worker_boot_script_mcpu"`
	WorkerBootScriptMem   proc.Tmem  `json:"worker_boot_script_mem"`
	FTTaskSrvMcpu         proc.Tmcpu `json:"ft_task_srv_mcpu"`
	ImgDim                int        `json:"img_dim"`
}

func NewImgdJobConfig(job string, workerMcpu proc.Tmcpu, workerMem proc.Tmem, persist bool, nrounds int, imgdMcpu proc.Tmcpu, useSPProxy bool, useBootScript bool, useS3Clnt bool, workerBootScriptMcpu proc.Tmcpu, workerBootScriptMem proc.Tmem, ftTaskSrvMcpu proc.Tmcpu) *ImgdJobConfig {
	return &ImgdJobConfig{
		Job:                  job,
		WorkerMcpu:           workerMcpu,
		WorkerMem:            workerMem,
		Persist:              persist,
		NRounds:              nrounds,
		ImgdMcpu:             imgdMcpu,
		UseSPProxy:           useSPProxy,
		UseBootScript:        useBootScript,
		UseS3Clnt:            useS3Clnt,
		WorkerBootScriptMcpu: workerBootScriptMcpu,
		WorkerBootScriptMem:  workerBootScriptMem,
		FTTaskSrvMcpu:        ftTaskSrvMcpu,
	}
}

func (cfg *ImgdJobConfig) String() string {
	return fmt.Sprintf("&{ job:%v workerMcpu:%v workerMem:%v persist:%v nrounds:%v imgdMcpu:%v useSPProxy:%v useBootScript:%v useS3Clnt:%v workerBootScriptMcpu:%v workerBootScriptMem:%v ftTaskSrvMcpu:%v imgDim:%v }", cfg.Job, cfg.WorkerMcpu, cfg.WorkerMem, cfg.Persist, cfg.NRounds, cfg.ImgdMcpu, cfg.UseSPProxy, cfg.UseBootScript, cfg.UseS3Clnt, cfg.WorkerBootScriptMcpu, cfg.WorkerBootScriptMem, cfg.FTTaskSrvMcpu, cfg.ImgDim)
}

func GetBootScriptWriteOut(sc *sigmaclnt.SigmaClnt) ([]byte, error) {
	return wasmer.ReadBootScript(sc, "imgprocess_write_out_boot")
}

func GetBootScript(sc *sigmaclnt.SigmaClnt) ([]byte, error) {
	return wasmer.ReadBootScript(sc, "s3get_boot")
}

func GetBootScriptInput(bucket, key, kid string) ([]byte, error) {
	inputBuf := bytes.NewBuffer(make([]byte, 0, 12+len(bucket)+len(key)+len(kid)))
	if err := binary.Write(inputBuf, binary.LittleEndian, uint32(len(bucket))); err != nil {
		return nil, err
	}
	if err := binary.Write(inputBuf, binary.LittleEndian, uint32(len(key))); err != nil {
		return nil, err
	}
	if err := binary.Write(inputBuf, binary.LittleEndian, uint32(len(kid))); err != nil {
		return nil, err
	}
	if n, err := inputBuf.Write([]byte(bucket)); err != nil || n != len(bucket) {
		return nil, fmt.Errorf("Err write bucket %v n %v", err, n)
	}
	if n, err := inputBuf.Write([]byte(key)); err != nil || n != len(key) {
		return nil, fmt.Errorf("Err write key %v n %v", err, n)
	}
	if n, err := inputBuf.Write([]byte(kid)); err != nil || n != len(kid) {
		return nil, fmt.Errorf("Err write kid %v n %v", err, n)
	}
	return inputBuf.Bytes(), nil
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
	UseBootScript         bool   `json:"use_boot_script"`
	WriteOutViaBootScript bool   `json:"write_out_via_boot_script"`
}

func NewTask(fn string, useS3Clnt, useBootScript, writeOutViaBootScript bool) *Ttask {
	return &Ttask{
		FileName:              fn,
		UseS3Clnt:             useS3Clnt,
		UseBootScript:         useBootScript,
		WriteOutViaBootScript: writeOutViaBootScript,
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

	cfg := procgroupmgr.NewProcGroupConfig(1, "imgresized", []string{strconv.Itoa(int(jobCfg.WorkerMcpu)), strconv.Itoa(int(jobCfg.WorkerMem)), strconv.Itoa(jobCfg.NRounds), TaskSvcId(imgd.job), strconv.FormatBool(jobCfg.UseSPProxy), strconv.FormatBool(jobCfg.UseBootScript), strconv.Itoa(int(jobCfg.WorkerBootScriptMcpu)), strconv.Itoa(int(jobCfg.WorkerBootScriptMem)), strconv.FormatBool(jobCfg.WriteOutViaBootScript), strconv.Itoa(jobCfg.ImgDim)}, jobCfg.ImgdMcpu, ImgSvcId(jobCfg.Job))

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

func GetMkProcFn(serverId task.FtTaskSvcId, nrounds int, imgDim int, workerMcpu proc.Tmcpu, workerMem proc.Tmem, workerBootScriptMcpu proc.Tmcpu, workerBootScriptMem proc.Tmem, bootScript []byte, bootScriptWriteOut []byte, useSPProxy bool) fttask_mgr.TnewProc[Ttask] {
	return func(task fttask_clnt.Task[Ttask]) (*proc.Proc, error) {
		db.DPrintf(db.IMGD, "mkProc %v", task)
		fn := task.Data.FileName
		p := proc.NewProcPid(sp.GenPid(string(serverId)), "imgresize", []string{fn, ThumbName(fn), strconv.Itoa(nrounds), strconv.FormatBool(task.Data.UseS3Clnt), strconv.FormatBool(task.Data.WriteOutViaBootScript), strconv.Itoa(imgDim)})
		p.SetMcpu(workerMcpu)
		p.SetMem(workerMem)
		splitFN := strings.Split(fn, "/")
		bootScriptInput, err := GetBootScriptInput(splitFN[0], filepath.Join(splitFN[1:]...), sp.LOCAL)
		if err != nil {
			db.DPrintf(db.ERROR, "Err get bootscript input")
			return nil, err
		}
		p.GetProcEnv().UseSPProxy = useSPProxy
		if task.Data.WriteOutViaBootScript {
			p.SetBootScript(bootScriptWriteOut, bootScriptInput)
		} else {
			p.SetBootScript(bootScript, bootScriptInput)
		}
		p.SetRunBootScript(task.Data.UseBootScript)
		if task.Data.UseBootScript {
			// Run after boot script, if we set a resource reservation for the boot
			// script
			if workerBootScriptMcpu > 0 || workerBootScriptMem > 0 {
				p.SetRunAfterBootScript(true)
			}
			p.SetBootScriptMcpu(workerBootScriptMcpu)
			p.SetBootScriptMem(workerBootScriptMem)
		}
		return p, nil
	}
}

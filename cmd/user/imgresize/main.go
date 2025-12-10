package main

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nfnt/resize"

	"sigmaos/api/fs"
	db "sigmaos/debug"
	"sigmaos/proc"
	s3clnt "sigmaos/proxy/s3/clnt"
	"sigmaos/rpc"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/util/crash"
	"sigmaos/util/perf"
)

const (
	IMG_DIM = 160
)

//
// Crop picture <in> to <out>
//

func main() {
	pe := proc.GetProcEnv()
	db.DPrintf(db.IMGD, "imgresize %v: %v", pe.GetPID(), os.Args)
	p, err := perf.NewPerf(pe, perf.THUMBNAIL)
	if err != nil {
		db.DFatalf("NewPerf err %v\n", err)
	}
	defer p.Done()

	ip, err := NewImgProcess(pe, os.Args, p)
	if err != nil {
		db.DFatalf("Error %v", err)
	}

	rand.Seed(time.Now().UnixNano())

	var s *proc.Status
	for i := 0; i < len(ip.inputs); i++ {
		start := time.Now()
		output := ip.output
		// Create a new file name for iterations > 0
		output += strconv.Itoa(rand.Int())
		s = ip.Work(i, output)
		db.DPrintf(db.ALWAYS, "Time %v e2e resize[%v]: %v", os.Args, i, time.Since(start))
	}
	db.DPrintf(db.ALWAYS, "Total time to completion since spawn: %v", time.Since(pe.GetSpawnTime()))
	execTimeStr := os.Getenv("SIGMA_EXEC_TIME")
	// If not set, bail out
	if execTimeStr == "" {
		return
	}
	execTimeMicro, err := strconv.ParseInt(execTimeStr, 10, 64)
	if err != nil {
		db.DFatalf("Error parsing exec time 2: %v", err)
		return
	}
	execTime := time.UnixMicro(execTimeMicro)
	db.DPrintf(db.ALWAYS, "Total time to completion since exec: %v", time.Since(execTime))
	ip.ClntExit(s)
}

type ImgProcess struct {
	*sigmaclnt.SigmaClnt
	inputs                []string
	output                string
	ctx                   fs.CtxI
	nrounds               int
	p                     *perf.Perf
	useS3Clnt             bool
	writeOutViaBootScript bool
	imgDim                int
	s3Clnt                *s3clnt.S3Clnt
}

func NewImgProcess(pe *proc.ProcEnv, args []string, p *perf.Perf) (*ImgProcess, error) {
	if len(args) != 7 {
		return nil, fmt.Errorf("NewImgProcess: wrong number of arguments: %v", args)
	}
	ip := &ImgProcess{
		p: p,
	}
	db.DPrintf(db.ALWAYS, "E2e spawn time since spawn until main: %v", time.Since(pe.GetSpawnTime()))
	sc, err := sigmaclnt.NewSigmaClnt(pe)
	if err != nil {
		return nil, err
	}
	useS3Clnt, err := strconv.ParseBool(args[4])
	if err != nil {
		db.DFatalf("Err parse useS3Clnt: %v", err)
	}
	writeOutViaBootScript, err := strconv.ParseBool(args[5])
	if err != nil {
		db.DFatalf("Err parse useS3Clnt: %v", err)
	}
	ip.useS3Clnt = useS3Clnt
	ip.writeOutViaBootScript = writeOutViaBootScript
	ip.SigmaClnt = sc
	ip.inputs = strings.Split(args[1], ",")
	db.DPrintf(db.ALWAYS, "Args {%v} inputs {%v} fail {%v}", args[1], ip.inputs, proc.GetSigmaFail())
	ip.output = ip.inputs[0] + "-thumbnail"
	ip.nrounds, err = strconv.Atoi(args[3])
	if err != nil {
		db.DFatalf("Err convert nrounds: %v", err)
	}
	ip.imgDim, err = strconv.Atoi(args[6])
	if err != nil {
		db.DFatalf("Err convert nrounds: %v", err)
	}
	ip.Started()
	crash.FailersDefault(sc.FsLib, []crash.Tselector{crash.IMGRESIZE_CRASH})
	if useS3Clnt {
		s3Clnt, err := s3clnt.NewS3Clnt(ip.FsLib, filepath.Join(sp.S3, pe.GetKernelID()))
		if err != nil {
			db.DFatalf("Err newS3Clnt: %v", err)
		}
		ip.s3Clnt = s3Clnt
	}
	return ip, nil
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error {
	return nil
}

func (ip *ImgProcess) Work(i int, output string) *proc.Status {
	db.DPrintf(db.ALWAYS, "Resize (%v/%v) %v s3rpc %v", i, len(ip.inputs), ip.inputs[i], ip.useS3Clnt)
	do := time.Now()
	var rdr io.ReadCloser
	var err error
	if ip.useS3Clnt {
		start := time.Now()
		s3pn := filepath.Join(sp.S3, sp.LOCAL)
		if ep, ok := ip.ProcEnv().GetCachedEndpoint(s3pn); ok {
			if err := ip.MountTree(ep, rpc.RPC, filepath.Join(s3pn, rpc.RPC)); err != nil {
				db.DFatalf("Err mount S3 rpc file: %v", err)
			}
		}
		db.DPrintf(db.ALWAYS, "Time %v mount S3: %v", ip.inputs[0], time.Since(start))
		pn := strings.Split(ip.inputs[i], "/")
		bucket := pn[0]
		key := filepath.Join(pn[1:]...)
		var b []byte
		var err error
		start = time.Now()
		if ip.ProcEnv().GetRunBootScript() {
			var transferTime time.Duration
			b, transferTime, err = ip.s3Clnt.DelegatedGetObject(0)
			if err != nil {
				return proc.NewStatusErr(fmt.Sprintf("Err GetObject bucket:%v key:%v", bucket, key), err)
			}
			db.DPrintf(db.ALWAYS, "Resize delegated get")
			db.DPrintf(db.ALWAYS, "Time %v sandboxTransfer: %v", ip.inputs[i], transferTime)
		} else {
			b, err = ip.s3Clnt.GetObject(bucket, key, false)
			if err != nil {
				return proc.NewStatusErr(fmt.Sprintf("Err GetObject bucket:%v key:%v", bucket, key), err)
			}
		}
		db.DPrintf(db.ALWAYS, "Time %v S3Get: %v", ip.inputs[i], time.Since(start))
		rdr = io.NopCloser(bytes.NewReader(b))
	} else {
		rdr, err = ip.OpenReader(ip.inputs[i])
		if err != nil {
			return proc.NewStatusErr(fmt.Sprintf("File %v not found kid %v", ip.inputs[i], ip.ProcEnv().GetKernelID()), err)
		}
	}
	//	prdr := perf.NewPerfReader(rdr, ip.p)
	db.DPrintf(db.ALWAYS, "Time %v open: %v", ip.inputs[i], time.Since(do))
	var dc time.Time
	defer func() {
		rdr.Close()
		db.DPrintf(db.ALWAYS, "Time %v close reader: %v", ip.inputs[i], time.Since(dc))
	}()

	ds := time.Now()
	img, err := jpeg.Decode(rdr)
	if err != nil {
		return proc.NewStatusErr("Decode", err)
	}
	// img size in bytes:
	bounds := img.Bounds()
	var imgSizeB uint64 = 16 * uint64(bounds.Max.X-bounds.Min.X) * uint64(bounds.Max.Y-bounds.Min.Y)
	db.DPrintf(db.ALWAYS, "Time %v read/decode: %v", ip.inputs[i], time.Since(ds))
	dr := time.Now()
	for i := 0; i < ip.nrounds-1; i++ {
		resize.Resize(uint(ip.imgDim), uint(ip.imgDim), img, resize.Lanczos3)
		ip.p.TptTick(float64(imgSizeB))
	}
	img1 := resize.Resize(uint(ip.imgDim), uint(ip.imgDim), img, resize.Lanczos3)
	ip.p.TptTick(float64(imgSizeB))
	db.DPrintf(db.ALWAYS, "Time %v resize: %v", ip.inputs[i], time.Since(dr))

	dcw := time.Now()
	var wrt io.WriteCloser
	var outbuf *bytes.Buffer
	if ip.useS3Clnt {
		outbuf = bytes.NewBuffer(nil)
		wrt = &nopWriteCloser{outbuf}
	} else {
		wrt, err = ip.CreateWriter(output, 0777, sp.OWRITE)
		if err != nil {
			return proc.NewStatusErr(fmt.Sprintf("Open output failed %v", output), err)
		}
	}
	//	pwrt := perf.NewPerfWriter(wrt, ip.p)
	db.DPrintf(db.ALWAYS, "Time %v create writer: %v", ip.inputs[i], time.Since(dcw))
	dw := time.Now()
	defer func() {
		wrt.Close()
		db.DPrintf(db.ALWAYS, "Time %v write/encode: %v", ip.inputs[i], time.Since(dw))
		dc = time.Now()
	}()

	jpeg.Encode(wrt, img1, nil)
	if ip.useS3Clnt {
		start := time.Now()
		pn := strings.Split(output, "/")
		bucket := pn[0]
		key := filepath.Join(pn[1:]...)
		if ip.writeOutViaBootScript {
			if err := ip.s3Clnt.DelegatedPutObject(1, bucket, key, outbuf.Bytes()); err != nil {
				return proc.NewStatusErr(fmt.Sprintf("Err PutObjectViaBootScript bucket:%v key:%v", bucket, key), err)
			}
		} else {
			if err := ip.s3Clnt.PutObject(bucket, key, outbuf.Bytes()); err != nil {
				return proc.NewStatusErr(fmt.Sprintf("Err PutObject bucket:%v key:%v", bucket, key), err)
			}
		}
		db.DPrintf(db.ALWAYS, "Time %v S3Put: %v", ip.inputs[i], time.Since(start))
	}
	return proc.NewStatus(proc.StatusOK)
}

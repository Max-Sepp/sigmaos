package imgrec_py_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	imgrectestutil "sigmaos/apps/imgrec/testutil"
	db "sigmaos/debug"
	"sigmaos/proc"
	wasmrt "sigmaos/proxy/wasm/rpc/wasmer"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/test"
)

const (
	IMG_BUCKET    = "9ps3"
	IMG_KEY       = "img-save/8.jpg"
	MODEL_BUCKET  = "9ps3"
	MODEL_KEY     = "mobilenetv2-12.onnx"
	KID           = "~local"
	PY_SCRIPT_DIR = "/home/sigmaos/bin/python"
)

func getImgrecCoSandbox(t *testing.T, sc *sigmaclnt.SigmaClnt) []byte {
	b, err := wasmrt.ReadCoSandbox(sc, "imgrec_boot")
	if !assert.Nil(t, err, "ReadCoSandbox imgrec_boot: %v", err) {
		t.FailNow()
	}
	return b
}

func TestImgrecPy(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)
	ref := imgrectestutil.GetReferenceOutput(t, rts.FsLib, IMG_BUCKET, IMG_KEY, MODEL_BUCKET, MODEL_KEY, KID)

	p := proc.NewProc("imgrec.py", []string{
		IMG_BUCKET, IMG_KEY,
		MODEL_BUCKET, MODEL_KEY,
		KID,
	})
	p.GetProcEnv().UseSPProxy = true
	p.GetProcEnv().UseSPProxyProcClnt = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_PYTHON)

	err = rts.Spawn(p)
	if !assert.Nil(t, err, "Spawn: %v", err) {
		return
	}

	err = rts.WaitStart(p.GetPid())
	if !assert.Nil(t, err, "WaitStart: %v", err) {
		return
	}

	status, err := rts.WaitExit(p.GetPid())
	assert.Nil(t, err, "WaitExit err: %v", err)
	assert.True(t, status.IsStatusOK(), "WaitExit status: %v", status)

	msg := status.Msg()
	db.DPrintf(db.TEST, "imgrec pred: %v", msg)
	imgrectestutil.AssertMatchesReference(t, msg, ref)
}

func TestImgrecPyCoSandbox(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)
	ref := imgrectestutil.GetReferenceOutput(t, rts.FsLib, IMG_BUCKET, IMG_KEY, MODEL_BUCKET, MODEL_KEY, KID)

	coSandbox := getImgrecCoSandbox(t, rts.SigmaClnt)
	// Boot script input: model=rpcIdx 0, image=rpcIdx 1.
	// imgrec.py detects RunCoSandbox and calls s3_delegated_get_object with
	// matching indices.
	bootInput := wasmrt.EncodeArgs([]string{IMG_BUCKET, IMG_KEY, MODEL_BUCKET, MODEL_KEY, KID})

	p := proc.NewProc("imgrec.py", []string{
		IMG_BUCKET, IMG_KEY,
		MODEL_BUCKET, MODEL_KEY,
		KID,
	})
	p.GetProcEnv().UseSPProxy = true
	p.GetProcEnv().UseSPProxyProcClnt = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_PYTHON)
	p.SetCoSandbox(coSandbox, bootInput)
	p.SetRunCoSandbox(true)

	err = rts.Spawn(p)
	if !assert.Nil(t, err, "Spawn: %v", err) {
		return
	}

	err = rts.WaitStart(p.GetPid())
	if !assert.Nil(t, err, "WaitStart: %v", err) {
		return
	}

	status, err := rts.WaitExit(p.GetPid())
	assert.Nil(t, err, "WaitExit err: %v", err)
	assert.True(t, status.IsStatusOK(), "WaitExit status: %v", status)

	msg := status.Msg()
	db.DPrintf(db.TEST, "imgrec pred: %v", msg)
	imgrectestutil.AssertMatchesReference(t, msg, ref)
}

func TestImgrecPyShmem(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)
	ref := imgrectestutil.GetReferenceOutput(t, rts.FsLib, IMG_BUCKET, IMG_KEY, MODEL_BUCKET, MODEL_KEY, KID)

	coSandbox := getImgrecCoSandbox(t, rts.SigmaClnt)
	bootInput := wasmrt.EncodeArgs([]string{IMG_BUCKET, IMG_KEY, MODEL_BUCKET, MODEL_KEY, KID})

	p := proc.NewProc("imgrec.py", []string{
		IMG_BUCKET, IMG_KEY,
		MODEL_BUCKET, MODEL_KEY,
		KID,
	})
	p.GetProcEnv().UseSPProxy = true
	p.GetProcEnv().UseSPProxyProcClnt = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_PYTHON)
	p.SetCoSandbox(coSandbox, bootInput)
	p.SetRunCoSandbox(true)
	p.SetShmemMB(proc.Tmem(256))

	err = rts.Spawn(p)
	if !assert.Nil(t, err, "Spawn: %v", err) {
		return
	}

	err = rts.WaitStart(p.GetPid())
	if !assert.Nil(t, err, "WaitStart: %v", err) {
		return
	}

	status, err := rts.WaitExit(p.GetPid())
	assert.Nil(t, err, "WaitExit err: %v", err)
	assert.True(t, status.IsStatusOK(), "WaitExit status: %v", status)

	msg := status.Msg()
	db.DPrintf(db.TEST, "imgrec pred: %v", msg)
	imgrectestutil.AssertMatchesReference(t, msg, ref)
}

package imgrec_wasm_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	imgrectestutil "sigmaos/apps/imgrec/testutil"
	db "sigmaos/debug"
	"sigmaos/proc"
	wasmrt "sigmaos/proxy/wasm/rpc/wasmer"
	"sigmaos/sigmaclnt"
	"sigmaos/sigmaclnt/fslib"
	sp "sigmaos/sigmap"
	"sigmaos/test"
)

const (
	IMG_BUCKET       = "9ps3"
	IMG_KEY          = "img-save/8.jpg"
	MODEL_BUCKET     = "9ps3"
	MODEL_KEY        = "mobilenetv2-12.onnx"
	KID              = "~local"
	PRECOMPILED_PROG = "imgrec_precompiled.wasm"
	LOCAL_WASM_BIN   = "../../../bin/wasm/imgrec.wasm"
)

// uploadBytes uploads in-memory bytes to a SigmaOS path in a streaming fashion,
// avoiding the size limit of PutFile.
func uploadBytes(fsl *fslib.FsLib, data []byte, spn sp.Tsigmapath) error {
	wrt, err := fsl.CreateWriter(spn, 0777, sp.OWRITE)
	if err != nil {
		return err
	}
	defer wrt.Close()
	_, err = io.Copy(wrt, bytes.NewReader(data))
	return err
}

func uploadImgrecBin(t *testing.T, rts *test.RealmTstate) string {
	var precompiledBinPath string
	if rts.ProcEnv().BuildTag == sp.LOCAL_BUILD {
		precompiledBinPath = filepath.Join(sp.S3, sp.ANY, "9ps3", PRECOMPILED_PROG+"-v"+sp.Version)
	} else {
		precompiledBinPath = filepath.Join(sp.S3, sp.ANY, rts.ProcEnv().BuildTag, PRECOMPILED_PROG)
	}
	db.DPrintf(db.TEST, "Upload compiled wasm bin to %v", precompiledBinPath)
	rawBytes, err := os.ReadFile(LOCAL_WASM_BIN)
	if !assert.Nil(t, err, "ReadFile %v: %v", LOCAL_WASM_BIN, err) {
		t.FailNow()
	}
	wrt := wasmrt.NewWasmerRuntime(nil)
	compiledBytes, err := wrt.PrecompileModule(rawBytes)
	if !assert.Nil(t, err, "PrecompileModule: %v", err) {
		t.FailNow()
	}
	rts.Remove(precompiledBinPath)
	err = uploadBytes(rts.FsLib, compiledBytes, precompiledBinPath)
	if !assert.Nil(t, err, "uploadBytes precompiled %v: %v", precompiledBinPath, err) {
		t.FailNow()
	}
	return precompiledBinPath
}

func getImgrecCoSandbox(t *testing.T, sc *sigmaclnt.SigmaClnt) []byte {
	b, err := wasmrt.ReadCoSandbox(sc, "imgrec_boot")
	if !assert.Nil(t, err, "ReadCoSandbox imgrec_boot: %v", err) {
		t.FailNow()
	}
	return b
}

func TestCompile(t *testing.T) {
}

func TestImgrec(t *testing.T) {
	mrts, err1 := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err1, "Error New Tstate: %v", err1) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)
	ref := imgrectestutil.GetReferenceOutput(t, rts.FsLib, IMG_BUCKET, IMG_KEY, MODEL_BUCKET, MODEL_KEY, KID)
	precompiledBinPath := uploadImgrecBin(t, rts)

	p := proc.NewProc(PRECOMPILED_PROG, []string{
		IMG_BUCKET, IMG_KEY,
		MODEL_BUCKET, MODEL_KEY,
		KID, "false", // use_delegated=false: direct S3 fetch
	})
	p.GetProcEnv().UseSPProxy = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_WASM)
	if rts.ProcEnv().BuildTag == sp.LOCAL_BUILD {
		p.PrependSigmaPath(filepath.Dir(precompiledBinPath))
	}

	err := rts.Spawn(p)
	assert.Nil(t, err, "Spawn")

	err = rts.WaitStart(p.GetPid())
	assert.Nil(t, err, "WaitStart error")

	status, err := rts.WaitExit(p.GetPid())
	assert.Nil(t, err, "WaitExit error %v", err)
	assert.True(t, status.IsStatusOK(), "WaitExit status error: %v", status)

	msg := status.Msg()
	db.DPrintf(db.TEST, "imgrec pred: %v", msg)
	imgrectestutil.AssertMatchesReference(t, msg, ref)
}

func TestImgrecWASMCoSandboxShmem(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)
	ref := imgrectestutil.GetReferenceOutput(t, rts.FsLib, IMG_BUCKET, IMG_KEY, MODEL_BUCKET, MODEL_KEY, KID)

	coSandbox := getImgrecCoSandbox(t, rts.SigmaClnt)
	bootInput := wasmrt.EncodeArgs([]string{IMG_BUCKET, IMG_KEY, MODEL_BUCKET, MODEL_KEY, KID})
	precompiledBinPath := uploadImgrecBin(t, rts)

	p := proc.NewProc(PRECOMPILED_PROG, []string{
		IMG_BUCKET, IMG_KEY,
		MODEL_BUCKET, MODEL_KEY,
		KID, "true", // use_delegated=true
	})
	p.GetProcEnv().UseSPProxy = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_WASM)
	p.SetCoSandbox(coSandbox, bootInput)
	p.SetRunCoSandbox(true)
	p.SetShmemMB(proc.Tmem(32))
	if rts.ProcEnv().BuildTag == sp.LOCAL_BUILD {
		p.PrependSigmaPath(filepath.Dir(precompiledBinPath))
	}

	err = rts.Spawn(p)
	assert.Nil(t, err, "Spawn")

	err = rts.WaitStart(p.GetPid())
	assert.Nil(t, err, "WaitStart error")

	status, err := rts.WaitExit(p.GetPid())
	assert.Nil(t, err, "WaitExit error %v", err)
	assert.True(t, status.IsStatusOK(), "WaitExit status error: %v", status)

	msg := status.Msg()
	db.DPrintf(db.TEST, "imgrec pred: %v", msg)
	imgrectestutil.AssertMatchesReference(t, msg, ref)
}

func TestImgrecWASMCoSandbox(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)
	ref := imgrectestutil.GetReferenceOutput(t, rts.FsLib, IMG_BUCKET, IMG_KEY, MODEL_BUCKET, MODEL_KEY, KID)

	// Read and precompile the boot script.
	coSandbox := getImgrecCoSandbox(t, rts.SigmaClnt)
	// Boot script input: model=rpcIdx 0, image=rpcIdx 1.
	bootInput := wasmrt.EncodeArgs([]string{IMG_BUCKET, IMG_KEY, MODEL_BUCKET, MODEL_KEY, KID})

	precompiledBinPath := uploadImgrecBin(t, rts)

	p := proc.NewProc(PRECOMPILED_PROG, []string{
		IMG_BUCKET, IMG_KEY,
		MODEL_BUCKET, MODEL_KEY,
		KID, "true", // use_delegated=true: retrieve from SPProxy store
	})
	p.GetProcEnv().UseSPProxy = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_WASM)
	p.SetCoSandbox(coSandbox, bootInput)
	p.SetRunCoSandbox(true)
	if rts.ProcEnv().BuildTag == sp.LOCAL_BUILD {
		p.PrependSigmaPath(filepath.Dir(precompiledBinPath))
	}

	err = rts.Spawn(p)
	assert.Nil(t, err, "Spawn")

	err = rts.WaitStart(p.GetPid())
	assert.Nil(t, err, "WaitStart error")

	status, err := rts.WaitExit(p.GetPid())
	assert.Nil(t, err, "WaitExit error %v", err)
	assert.True(t, status.IsStatusOK(), "WaitExit status error: %v", status)

	msg := status.Msg()
	db.DPrintf(db.TEST, "imgrec pred: %v", msg)
	imgrectestutil.AssertMatchesReference(t, msg, ref)
}

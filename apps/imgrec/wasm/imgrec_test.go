package imgrec_wasm_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	db "sigmaos/debug"
	"sigmaos/proc"
	wasmrt "sigmaos/proxy/wasm/rpc/wasmer"
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

func TestCompile(t *testing.T) {
}

func TestImgrec(t *testing.T) {
	mrts, err1 := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err1, "Error New Tstate: %v", err1) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)

	// Compute the upload path using the realm's origin SigmaPath entry
	//	sigmaPath := rts.ProcEnv().GetSigmaPath()
	//	originDir := sigmaPath[len(sigmaPath)-1]
	//	precompiledBinPath := filepath.Join(originDir, PRECOMPILED_PROG+"-v"+sp.Version)
	precompiledBinPath := filepath.Join(sp.S3, sp.ANY, "9ps3", PRECOMPILED_PROG+"-v"+sp.Version)

	db.DPrintf(db.TEST, "Upload compiled wasm bin to %v", precompiledBinPath)

	// Read the raw WASM binary from the local bin directory for precompilation
	rawBytes, err := os.ReadFile(LOCAL_WASM_BIN)
	if !assert.Nil(t, err, "ReadFile %v: %v", LOCAL_WASM_BIN, err) {
		return
	}

	// Precompile the WASM module using the Cranelift compiler
	wrt := wasmrt.NewWasmerRuntime(nil)
	compiledBytes, err := wrt.PrecompileModule(rawBytes)
	if !assert.Nil(t, err, "PrecompileModule: %v", err) {
		return
	}

	// Remove any previously uploaded precompiled binary before uploading
	rts.Remove(precompiledBinPath)

	// Upload the precompiled binary to the const path in SigmaOS
	err = uploadBytes(rts.FsLib, compiledBytes, precompiledBinPath)
	if !assert.Nil(t, err, "uploadBytes precompiled %v: %v", precompiledBinPath, err) {
		return
	}

	p := proc.NewProc(PRECOMPILED_PROG, []string{
		IMG_BUCKET, IMG_KEY,
		MODEL_BUCKET, MODEL_KEY,
		KID,
	})
	p.GetProcEnv().UseSPProxy = true
	p.SetProcContainerType(proc.ProcContainerType_PROC_CTR_WASM)
	// XXX hack, remove eventually
	p.PrependSigmaPath(filepath.Dir(precompiledBinPath))

	err = rts.Spawn(p)
	assert.Nil(t, err, "Spawn")

	err = rts.WaitStart(p.GetPid())
	assert.Nil(t, err, "WaitStart error")

	status, err := rts.WaitExit(p.GetPid())
	assert.Nil(t, err, "WaitExit error %v", err)
	assert.True(t, status.IsStatusOK(), "WaitExit status error: %v", status)

	// The exit message should be "class_idx,score"
	msg := status.Msg()
	assert.True(t, strings.Contains(msg, ","), "exit msg should be class_idx,score: %v", msg)
}

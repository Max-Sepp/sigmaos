package imgrec_py_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"sigmaos/proc"
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

func TestImgrecPy(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	rts := mrts.GetRealm(test.REALM1)

	p := proc.NewProc("imgrec.py", []string{
		IMG_BUCKET, IMG_KEY,
		MODEL_BUCKET, MODEL_KEY,
		KID,
	})
	p.GetProcEnv().UseSPProxy = true
	p.GetProcEnv().UseSPProxyProcClnt = true
	p.GetProcEnv().IsPythonProc = true

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
	assert.True(t, strings.Contains(msg, ","), "exit msg should be class_idx,score: %v", msg)
}

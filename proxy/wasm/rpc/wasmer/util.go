package wasmer

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"

	db "sigmaos/debug"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
)

// EncodeArgs encodes a slice of strings as N u32 LE lengths followed by N
// string bodies — the format expected by WASM boot/proc input buffers.
func EncodeArgs(args []string) []byte {
	total := 4 * len(args)
	for _, a := range args {
		total += len(a)
	}
	buf := make([]byte, 0, total)
	for _, a := range args {
		var lb [4]byte
		binary.LittleEndian.PutUint32(lb[:], uint32(len(a)))
		buf = append(buf, lb[:]...)
	}
	for _, a := range args {
		buf = append(buf, a...)
	}
	return buf
}

func projectRootPath() string {
	_, b, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(b)))))
}

func UploadCoSandboxRemote(sc *sigmaclnt.SigmaClnt, scriptName string) error {
	pn := filepath.Join(
		projectRootPath(),
		"bin/wasm",
		scriptName+".wasm",
	)
	db.DPrintf(db.ALWAYS, "CoSandbox path: %v", pn)
	pnRemote := filepath.Join(sp.S3, sp.ANY, sc.ProcEnv().BuildTag, "wasm", scriptName+".wasm")
	if err := sc.UploadFile(pn, pnRemote); err != nil {
		db.DPrintf(db.ERROR, "Err upload boot script (%v -> %v): %v", pn, pnRemote, err)
		return err
	}
	return nil
}

func ReadCoSandboxRemote(sc *sigmaclnt.SigmaClnt, scriptName string) ([]byte, error) {
	// Else, read it out of S3
	pn := filepath.Join(sp.S3, sp.ANY, sc.ProcEnv().BuildTag, "wasm", scriptName+".wasm")
	b, err := sc.GetFile(pn)
	if err != nil {
		db.DPrintf(db.ERROR, "Err read boot script remote (%v): %v", pn, err)
		return nil, err
	}
	wrt := NewWasmerRuntime(nil)
	return wrt.PrecompileModule(b)
}

func ReadCoSandbox(sc *sigmaclnt.SigmaClnt, scriptName string) ([]byte, error) {
	var b []byte
	var err error
	// If this is a local build, get the script from the local filesystem
	if sc.ProcEnv().BuildTag == sp.LOCAL_BUILD {
		// Compute WASM binary path name
		pn := filepath.Join(
			projectRootPath(),
			"bin/wasm",
			scriptName+".wasm",
		)
		db.DPrintf(db.ALWAYS, "CoSandbox path: %v", pn)
		if b, err = os.ReadFile(pn); err != nil {
			db.DPrintf(db.ERROR, "Err read boot script local: %v", err)
			return nil, err
		}
		wrt := NewWasmerRuntime(nil)
		return wrt.PrecompileModule(b)
	} else {
		return ReadCoSandboxRemote(sc, scriptName)
	}
}

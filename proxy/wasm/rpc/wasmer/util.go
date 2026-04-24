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

func UploadBootScriptRemote(sc *sigmaclnt.SigmaClnt, scriptName string) error {
	// Compute WASM binary path name
	pn := filepath.Join(
		projectRootPath(),
		"bin/wasm",
		scriptName+".wasm",
	)
	db.DPrintf(db.ALWAYS, "Boot script path: %v", pn)
	b, err := os.ReadFile(pn)
	if err != nil {
		db.DPrintf(db.ERROR, "Err read boot script local: %v", err)
		return err
	}
	pnRemote := filepath.Join(sp.S3, sp.ANY, sc.ProcEnv().BuildTag, "wasm", scriptName+".wasm")
	if _, err = sc.PutFile(pnRemote, 0777, sp.OWRITE, b); err != nil {
		db.DPrintf(db.ERROR, "Err write boot script remote (%v): %v", pn, err)
		return err
	}
	return nil
}

func ReadBootScriptRemote(sc *sigmaclnt.SigmaClnt, scriptName string) ([]byte, error) {
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

func ReadBootScript(sc *sigmaclnt.SigmaClnt, scriptName string) ([]byte, error) {
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
		db.DPrintf(db.ALWAYS, "Boot script path: %v", pn)
		if b, err = os.ReadFile(pn); err != nil {
			db.DPrintf(db.ERROR, "Err read boot script local: %v", err)
			return nil, err
		}
		wrt := NewWasmerRuntime(nil)
		return wrt.PrecompileModule(b)
	} else {
		return ReadBootScriptRemote(sc, scriptName)
	}
}

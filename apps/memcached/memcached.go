package memcached

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"sigmaos/proxy/wasm/rpc/wasmer"
	"sigmaos/sigmaclnt"
)

// For now, reuse imgprocess boot script
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

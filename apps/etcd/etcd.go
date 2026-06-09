package etcd

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"sigmaos/proxy/wasm/rpc/wasmer"
	"sigmaos/sigmaclnt"
)

func GetCoSandbox(sc *sigmaclnt.SigmaClnt) ([]byte, error) {
	return wasmer.ReadCoSandbox(sc, "s3get_boot")
}

func GetCoSandboxUX(sc *sigmaclnt.SigmaClnt) ([]byte, error) {
	return wasmer.ReadCoSandbox(sc, "uxget_boot")
}

func GetCoSandboxInput(bucket, key, kid string) ([]byte, error) {
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

func GetCoSandboxUXInput(path, kid string) ([]byte, error) {
	inputBuf := bytes.NewBuffer(make([]byte, 0, 8+len(path)+len(kid)))
	if err := binary.Write(inputBuf, binary.LittleEndian, uint32(len(path))); err != nil {
		return nil, err
	}
	if err := binary.Write(inputBuf, binary.LittleEndian, uint32(len(kid))); err != nil {
		return nil, err
	}
	if n, err := inputBuf.Write([]byte(path)); err != nil || n != len(path) {
		return nil, fmt.Errorf("Err write path %v n %v", err, n)
	}
	if n, err := inputBuf.Write([]byte(kid)); err != nil || n != len(kid) {
		return nil, fmt.Errorf("Err write kid %v n %v", err, n)
	}
	return inputBuf.Bytes(), nil
}

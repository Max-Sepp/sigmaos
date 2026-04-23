package rpc

import (
	db "sigmaos/debug"
	sessp "sigmaos/session/proto"
)

type Tstatus uint64

const (
	EXIT_OK           Tstatus = 0
	EXIT_ERR          Tstatus = 1
	EXIT_ABORT_LAUNCH Tstatus = 2
)

func (ts Tstatus) String() string {
	switch ts {
	case EXIT_OK:
		return "EXIT_OK"
	case EXIT_ABORT_LAUNCH:
		return "EXIT_ABORT_LAUNCH"
	case EXIT_ERR:
		return "EXIT_ERR"
	default:
		db.DFatalf("Err unknown WASM module status: %d", uint64(ts))
		return "unknown"
	}
}

type CoSandboxAPI interface {
	Send(rpcIdx uint64, pn string, method string, b []byte, nOutIOV uint64) error
	Recv(rpcIdx uint64, getData bool) (*sessp.IoVec, error)
	Forward(rpcIdx uint64, newRPCIdx uint64, pn string, nOutIOV uint64) error
	Exit(status Tstatus, msg string) error
	Log(msg string) error
}

type CoSandboxAPIImpl interface {
	WaitExit() (Tstatus, string, error)
	CoSandboxAPI
}

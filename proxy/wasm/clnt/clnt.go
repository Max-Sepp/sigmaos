package clnt

import (
	"fmt"

	db "sigmaos/debug"
	"sigmaos/proc"
	wasmproto "sigmaos/proxy/wasm/proto"
	rpcclnt "sigmaos/rpc/clnt"
	rpcnc "sigmaos/rpc/clnt/netconn"
	sp "sigmaos/sigmap"
)

type WASMClnt struct {
	rpcc *rpcclnt.RPCClnt
}

func NewWASMClnt() (*WASMClnt, error) {
	rpcc, err := rpcnc.NewUnixRPCClnt("wasmd", sp.WASMD_SOCKET)
	if err != nil {
		db.DPrintf(db.WASMD_ERR, "WASMClnt dial err: %v", err)
		return nil, err
	}
	return &WASMClnt{rpcc: rpcc}, nil
}

func (wc *WASMClnt) RunWASMProc(p *proc.Proc) (uint64, string, error) {
	req := &wasmproto.RunWASMProcReq{
		Proc: p.GetProto(),
	}
	rep := &wasmproto.RunWASMProcRep{}
	if err := wc.rpcc.RPC("WASMSrvAPI.RunWASMProc", req, rep); err != nil {
		db.DPrintf(db.WASMD_ERR, "WASMClnt.RunWASMProc RPC err: %v", err)
		return 0, "", err
	}
	db.DPrintf(db.WASMD, "WASMClnt.RunWASMProc done status=%v msg=%v err=%v", rep.Status, rep.Msg, rep.Err)
	var err error
	if rep.Err != "" {
		err = fmt.Errorf("%s", rep.Err)
	}
	return rep.Status, rep.Msg, err
}

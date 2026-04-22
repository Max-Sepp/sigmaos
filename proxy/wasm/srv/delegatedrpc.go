package srv

import (
	"time"

	db "sigmaos/debug"
	"sigmaos/proc"
	sessp "sigmaos/session/proto"
	"sigmaos/sigmaclnt"
)

func (ws *WASMSrv) runDelegatedRPC(sc *sigmaclnt.SigmaClnt, p *proc.Proc, rpcReps *RPCState, rpcIdx uint64, pn string, iniov *sessp.IoVec, outIOVSize uint64) {
	db.DPrintf(db.WASMD, "[%v] Get RPCChannel for delegated RPC(%v): %v", p.GetPid(), rpcIdx, pn)
	rpcchan, err := rpcReps.GetRPCChannel(sc, rpcIdx, pn)
	if err != nil {
		db.DFatalf("[%v] Err make delegated RPC(%v) channel pn:%v err:%v", p.GetPid(), rpcIdx, pn, err)
	}
	db.DPrintf(db.WASMD, "[%v] Run delegated RPC(%v)", p.GetPid(), rpcIdx)
	outiov := sessp.NewUnallocatedIoVec(int(outIOVSize), nil)
	start := time.Now()
	if err := rpcchan.SendReceive(iniov, outiov); err != nil {
		db.DPrintf(db.WASMD_ERR, "Err execute delegated RPC (%v): %v", pn, err)
		db.DFatalf("Err execute delegated RPC (%v): %v", pn, err)
	}
	db.DPrintf(db.WASMD, "[%v] Done running delegated RPC(%v) lat=%v", p.GetPid(), rpcIdx, time.Since(start))
	rpcReps.InsertReply(rpcIdx, outiov, err)
}

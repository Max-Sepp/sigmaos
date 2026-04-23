package srv

import (
	"time"

	db "sigmaos/debug"
	"sigmaos/proc"
	sessp "sigmaos/session/proto"
	"sigmaos/sigmaclnt"
)

func (ws *WASMSrv) runWASMProcRPC(sc *sigmaclnt.SigmaClnt, p *proc.Proc, rpcReps *RPCState, rpcIdx uint64, pn string, iniov *sessp.IoVec, outIOVSize uint64) {
	db.DPrintf(db.WASMD, "[%v] Get RPCChannel for WASM proc RPC(%v): %v", p.GetPid(), rpcIdx, pn)
	rpcchan, err := rpcReps.GetRPCChannel(sc, rpcIdx, pn)
	if err != nil {
		db.DFatalf("[%v] Err make WASM proc RPC(%v) channel pn:%v err:%v", p.GetPid(), rpcIdx, pn, err)
	}
	db.DPrintf(db.WASMD, "[%v] Run WASM proc RPC(%v)", p.GetPid(), rpcIdx)
	outiov := sessp.NewUnallocatedIoVec(int(outIOVSize), nil)
	start := time.Now()
	if err := rpcchan.SendReceive(iniov, outiov); err != nil {
		db.DPrintf(db.WASMD_ERR, "Err execute WASM proc RPC (%v): %v", pn, err)
		db.DFatalf("Err execute WASM proc RPC (%v): %v", pn, err)
	}
	lens := make([]int, outIOVSize)
	for i := range lens {
		lens[i] = outiov.GetFrame(i).Len()
	}
	db.DPrintf(db.WASMD, "[%v] Done running WASM proc RPC(%v) lat=%v lens=%v", p.GetPid(), rpcIdx, time.Since(start), lens)
	rpcReps.InsertReply(rpcIdx, outiov, err)
}

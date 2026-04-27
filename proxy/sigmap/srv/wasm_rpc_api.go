package srv

import (
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	db "sigmaos/debug"
	"sigmaos/proc"
	wasmrpc "sigmaos/proxy/wasm/rpc"
	rpcclnt "sigmaos/rpc/clnt"
	rpcproto "sigmaos/rpc/proto"
	"sigmaos/serr"
	sessp "sigmaos/session/proto"
	sigmaclnt "sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/util/perf"
)

type WASMRPCProxy struct {
	mu         sync.Mutex
	cond       *sync.Cond
	wg         sync.WaitGroup
	spp        *SPProxySrv
	sc         *sigmaclnt.SigmaClnt
	p          *proc.Proc
	exitStatus wasmrpc.Tstatus
	exitMsg    string
	exitErr    error
	exited     bool
}

func NewWASMRPCProxy(spp *SPProxySrv, sc *sigmaclnt.SigmaClnt, p *proc.Proc) wasmrpc.CoSandboxAPIImpl {
	wp := &WASMRPCProxy{
		spp: spp,
		sc:  sc,
		p:   p,
	}
	wp.cond = sync.NewCond(&wp.mu)
	return wp
}

func (wp *WASMRPCProxy) Send(rpcIdx uint64, pn string, method string, b []byte, nOutIOV uint64) error {
	// Copy the data, because the shared buffer pointed to by b may be
	// overwritten by the next asynchronous RPC
	reqBytes := make([]byte, len(b))
	copy(reqBytes, b)
	// Wrap the marshaled RPC byte slice in an RPC wrapper
	iniov, err := rpcclnt.WrapMarshaledRPCRequest(method, sessp.NewIoVec([][]byte{reqBytes}, nil))
	if err != nil {
		db.DPrintf(db.SPPROXYSRV_ERR, "[%v] Error wrap & marshal WASM-proxied RPC request: %v", wp.p.GetPid(), err)
		return err
	}
	wp.wg.Add(1)
	// Run the delegated RPC asynchronously, and add an extra output IOVec slot
	// for the RPC wrapper
	go func() {
		defer wp.wg.Done()
		wp.spp.runDelegatedRPC(wp.sc, wp.p, rpcIdx, pn, iniov, nOutIOV+1)
	}()
	return nil
}

func (wp *WASMRPCProxy) Recv(rpcIdx uint64, getData bool) (*sessp.IoVec, error) {
	outiov, err := wp.spp.psm.GetReply(wp.p.GetPid(), rpcIdx)
	if err != nil {
		db.DPrintf(db.SPPROXYSRV_ERR, "[%v] Err GetReply(%v) in WasmRPCRecv: %v", wp.p.GetPid(), err)
		return nil, err
	}
	// If the wasm module doesn't want the data back, bail out
	if !getData {
		return nil, nil
	}
	// Remove the RPC wrapper
	rep := &rpcproto.Rep{}
	if err := proto.Unmarshal(outiov.GetFrame(0).GetBuf(), rep); err != nil {
		db.DPrintf(db.SPPROXYSRV_ERR, "[%v] Err Unmarshal(%v) in WasmRPCRecv: %v", wp.p.GetPid(), err)
		return nil, serr.NewErrError(err)
	}
	if rep.Err.ErrCode != 0 {
		db.DPrintf(db.SPPROXYSRV_ERR, "[%v] Err ErrCode(%v) in WasmRPCRecv: %v", wp.p.GetPid(), rep.Err.ErrCode)
		return nil, sp.NewErr(rep.Err)
	}
	db.DPrintf(db.SPPROXYSRV, "[%v] RPC(%v) outiov len: %v", wp.p.GetPid(), outiov.Len()-1)
	return outiov, nil
}

func (wp *WASMRPCProxy) Forward(rpcIdx uint64, newRPCIdx uint64, pn string, nOutIOV uint64) error {
	// Get the RPC to forward
	iniov, err := wp.spp.psm.GetReply(wp.p.GetPid(), rpcIdx)
	if err != nil {
		db.DPrintf(db.SPPROXYSRV_ERR, "[%v] Error GetReply to forward for WASM-proxied RPC request: %v", wp.p.GetPid(), err)
		return err
	}
	wp.wg.Add(1)
	// Forward the delegated RPC synchronously, and add an extra output IOVec slot
	// for the RPC wrapper
	defer wp.wg.Done()
	wp.spp.runDelegatedRPC(wp.sc, wp.p, newRPCIdx, pn, iniov, nOutIOV+1)
	return nil
}

func (wp *WASMRPCProxy) Log(msg string) error {
	db.DPrintf(db.SPPROXYSRV, "[%v] WASM log: %v", wp.p.GetPid(), msg)
	return nil
}

func (wp *WASMRPCProxy) LogSpawnLatency(label string, elapsedMicros uint64) error {
	opStart := time.Now().Add(-time.Duration(elapsedMicros) * time.Microsecond)
	perf.LogSpawnLatency(label, wp.p.GetPid(), wp.p.GetSpawnTime(), opStart)
	return nil
}

func (wp *WASMRPCProxy) Exit(status wasmrpc.Tstatus, msg string) error {
	db.DPrintf(db.SPPROXYSRV, "[%v] CoSandbox called exited status %v msg %v", wp.p.GetPid(), status, msg)
	wp.mu.Lock()
	defer wp.mu.Unlock()

	wp.exitStatus = status
	wp.exitMsg = msg
	wp.exitErr = nil
	wp.exited = true
	wp.cond.Broadcast()

	db.DPrintf(db.SPPROXYSRV, "[%v] CoSandbox exited RPCs done status %v msg %v", wp.p.GetPid(), status, msg)
	return nil
}

func (wp *WASMRPCProxy) WaitExit() (wasmrpc.Tstatus, string, error) {
	db.DPrintf(db.SPPROXYSRV, "[%v] CoSandbox WaitExit", wp.p.GetPid())
	// Wait for any outstanding RPCs
	wp.wg.Wait()

	wp.mu.Lock()
	defer wp.mu.Unlock()

	for !wp.exited {
		wp.cond.Wait()
	}
	db.DPrintf(db.SPPROXYSRV, "[%v] CoSandbox WaitExit done", wp.p.GetPid())
	return wp.exitStatus, wp.exitMsg, wp.exitErr
}

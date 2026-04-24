package srv

import (
	"sync"

	"google.golang.org/protobuf/proto"

	db "sigmaos/debug"
	"sigmaos/proc"
	wasmrpc "sigmaos/proxy/wasm/rpc"
	delegatedrpcclnt "sigmaos/rpc/clnt/delegated"
	rpcclnt "sigmaos/rpc/clnt"
	rpcproto "sigmaos/rpc/proto"
	"sigmaos/serr"
	sessp "sigmaos/session/proto"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
)

// WASMProcAPIImpl implements CoSandboxAPIImpl for wasmd-run procs.
// It mirrors WASMRPCProxy in proxy/sigmap/srv.
type WASMProcAPIImpl struct {
	mu      sync.Mutex
	cond    *sync.Cond
	wg      sync.WaitGroup
	ws      *WASMSrv
	sc      *sigmaclnt.SigmaClnt
	p       *proc.Proc
	rpcReps *RPCState
	dc      *delegatedrpcclnt.Clnt
	exited  bool
	status  wasmrpc.Tstatus
	msg     string
	exitErr error
}

func NewWASMProcAPIImpl(ws *WASMSrv, sc *sigmaclnt.SigmaClnt, p *proc.Proc, rpcReps *RPCState) *WASMProcAPIImpl {
	impl := &WASMProcAPIImpl{
		ws:      ws,
		sc:      sc,
		p:       p,
		rpcReps: rpcReps,
		// TODO(shmem): pass shmem segment once wasmd shmem support is wired up.
		dc: delegatedrpcclnt.NewClnt(sc.FsLib, nil),
	}
	impl.cond = sync.NewCond(&impl.mu)
	return impl
}

// RecvDelegated retrieves a delegated RPC reply from SPProxy's store.
// Works for any service (S3, UX, etc.) whose reply follows the convention:
// bool oK = 1; Blob blob = 2.
func (impl *WASMProcAPIImpl) RecvDelegated(rpcIdx uint64) ([]byte, error) {
	return impl.dc.GetBytes(rpcIdx)
}

func (impl *WASMProcAPIImpl) Send(rpcIdx uint64, pn string, method string, b []byte, nOutIOV uint64) error {
	reqBytes := make([]byte, len(b))
	copy(reqBytes, b)
	iniov, err := rpcclnt.WrapMarshaledRPCRequest(method, sessp.NewIoVec([][]byte{reqBytes}, nil))
	if err != nil {
		db.DPrintf(db.WASMD_ERR, "[%v] Error wrap & marshal WASM RPC request: %v", impl.p.GetPid(), err)
		return err
	}
	impl.wg.Add(1)
	go func() {
		defer impl.wg.Done()
		impl.ws.runWASMProcRPC(impl.sc, impl.p, impl.rpcReps, rpcIdx, pn, iniov, nOutIOV+1)
	}()
	return nil
}

func (impl *WASMProcAPIImpl) Recv(rpcIdx uint64, getData bool) (*sessp.IoVec, error) {
	outiov, err := impl.rpcReps.GetReply(rpcIdx)
	if err != nil {
		db.DPrintf(db.WASMD_ERR, "[%v] Err GetReply(%v) in WASMRecv: %v", impl.p.GetPid(), rpcIdx, err)
		return nil, err
	}
	if !getData {
		return nil, nil
	}
	rep := &rpcproto.Rep{}
	if err := proto.Unmarshal(outiov.GetFrame(0).GetBuf(), rep); err != nil {
		db.DPrintf(db.WASMD_ERR, "[%v] Err Unmarshal in WASMRecv: %v", impl.p.GetPid(), err)
		return nil, serr.NewErrError(err)
	}
	if rep.Err.ErrCode != 0 {
		db.DPrintf(db.WASMD_ERR, "[%v] Err ErrCode in WASMRecv: %v", impl.p.GetPid(), rep.Err.ErrCode)
		return nil, sp.NewErr(rep.Err)
	}
	return outiov, nil
}

func (impl *WASMProcAPIImpl) Forward(rpcIdx uint64, newRPCIdx uint64, pn string, nOutIOV uint64) error {
	iniov, err := impl.rpcReps.GetReply(rpcIdx)
	if err != nil {
		db.DPrintf(db.WASMD_ERR, "[%v] Error GetReply to forward: %v", impl.p.GetPid(), err)
		return err
	}
	impl.wg.Add(1)
	defer impl.wg.Done()
	impl.ws.runWASMProcRPC(impl.sc, impl.p, impl.rpcReps, newRPCIdx, pn, iniov, nOutIOV+1)
	return nil
}

func (impl *WASMProcAPIImpl) Log(msg string) error {
	db.DPrintf(db.WASMD, "[%v] WASM log: %v", impl.p.GetPid(), msg)
	return nil
}

func (impl *WASMProcAPIImpl) Exit(status wasmrpc.Tstatus, msg string) error {
	db.DPrintf(db.WASMD, "[%v] WASMProc exited status %v msg %v", impl.p.GetPid(), status, msg)
	impl.mu.Lock()
	defer impl.mu.Unlock()
	impl.status = status
	impl.msg = msg
	impl.exited = true
	impl.cond.Broadcast()
	return nil
}

func (impl *WASMProcAPIImpl) WaitExit() (wasmrpc.Tstatus, string, error) {
	db.DPrintf(db.WASMD, "[%v] WASMProc WaitExit", impl.p.GetPid())
	impl.wg.Wait()
	impl.mu.Lock()
	defer impl.mu.Unlock()
	for !impl.exited {
		impl.cond.Wait()
	}
	db.DPrintf(db.WASMD, "[%v] WASMProc WaitExit done", impl.p.GetPid())
	return impl.status, impl.msg, impl.exitErr
}

package clnt

import (
	"sigmaos/proc"
	sp "sigmaos/sigmap"
)

// WASMContainer implements container.ProcContainer for WASM procs.
// It wraps a RunWASMProc call so WASM procs flow through the same
// ctr.Wait()/PSS/cleanup path in RunProc as SigmaContainer procs.
type WASMContainer struct {
	wc    *WASMClnt
	uproc *proc.Proc
	waitC chan error
}

// StartWASMContainer creates a WASMClnt, finalizes the proc env, and
// begins running the WASM proc asynchronously. Analogous to
// StartSigmaContainer.
func StartWASMContainer(uproc *proc.Proc, innerIP sp.Tip, outerIP sp.Tip, procdPid sp.Tpid) (*WASMContainer, error) {
	uproc.FinalizeEnv(innerIP, outerIP, procdPid)
	wc, err := NewWASMClnt()
	if err != nil {
		return nil, err
	}
	wc2 := &WASMContainer{
		wc:    wc,
		uproc: uproc,
		waitC: make(chan error, 1),
	}
	go func() {
		_, _, err := wc.RunWASMProc(uproc)
		wc2.waitC <- err
	}()
	return wc2, nil
}

// Pid returns 0 — WASM procs run inside wasmd and have no direct OS pid.
func (wc *WASMContainer) Pid() int {
	return 0
}

// GetPSS returns 0 — PSS measurement is not meaningful for WASM procs.
func (wc *WASMContainer) GetPSS() (proc.Tmem, error) {
	return 0, nil
}

// Wait blocks until the WASM proc finishes.
func (wc *WASMContainer) Wait() error {
	return <-wc.waitC
}

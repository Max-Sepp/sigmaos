// Package delegated provides a service-agnostic client for fetching delegated
// RPC replies from SPProxy. Any blob-returning service (S3, UX, etc.) is
// supported, provided its reply proto follows the convention:
//
//	bool oK = 1; Blob blob = 2;
package delegated

import (
	"sync"

	rpcclnt "sigmaos/rpc/clnt"
	rpcclntopts "sigmaos/rpc/clnt/opts"
	sprpcclnt "sigmaos/rpc/clnt/sigmap"
	rpcproto "sigmaos/rpc/proto"
	"sigmaos/shmem"
	"sigmaos/sigmaclnt/fslib"
)

// Clnt fetches delegated RPC replies from SPProxy without knowing which
// service originated the RPC. It holds a single RPCClnt initialized with only
// the SPProxy delegated channel — no service-specific channel needed.
type Clnt struct {
	once     sync.Once
	rpcc     *rpcclnt.RPCClnt
	fsl      *fslib.FsLib
	shmemSeg *shmem.Segment // nil until shmem support is added
}

func NewClnt(fsl *fslib.FsLib, shmemSeg *shmem.Segment) *Clnt {
	return &Clnt{fsl: fsl, shmemSeg: shmemSeg}
}

func (dc *Clnt) init() error {
	var initErr error
	dc.once.Do(func() {
		opts := []*rpcclntopts.RPCClntOption{
			sprpcclnt.WithDelegatedSPProxyChannel(dc.fsl),
		}
		// TODO(shmem): when adding shmem support, also pass:
		//   rpcclntopts.WithShmem(dc.shmemSeg)
		// and update GetBytes to handle the shmem reply path in DelegatedRPC.
		dc.rpcc, initErr = rpcclnt.NewRPCClnt("", opts...)
	})
	return initErr
}

// GetBytes fetches the delegated RPC reply stored at rpcIdx in SPProxy and
// returns the raw blob bytes. Compatible with any service whose reply follows
// the convention: bool oK = 1; Blob blob = 2 (e.g., S3Rep, UXRep).
func (dc *Clnt) GetBytes(rpcIdx uint64) ([]byte, error) {
	if err := dc.init(); err != nil {
		return nil, err
	}
	res := &rpcproto.GenericBlobRep{
		Blob: &rpcproto.Blob{Iov: [][]byte{{}}},
	}
	// TODO(shmem): for the shmem path, res.Blob.Iov must be pre-sized to
	// match the shmem layout expected by DelegatedRPC's shmem branch, and
	// dc.rpcc must be initialized with WithShmem.
	if _, err := dc.rpcc.DelegatedRPC(rpcIdx, res); err != nil {
		return nil, err
	}
	return res.Blob.Iov[0], nil
}

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
	"sigmaos/sigmaclnt/fslib"
)

// Clnt fetches delegated RPC replies from SPProxy without knowing which
// service originated the RPC. It holds a single RPCClnt initialized with only
// the SPProxy delegated channel — no service-specific channel needed.
type Clnt struct {
	once sync.Once
	rpcc *rpcclnt.RPCClnt
	fsl  *fslib.FsLib
}

func NewClnt(fsl *fslib.FsLib) *Clnt {
	return &Clnt{fsl: fsl}
}

func (dc *Clnt) init() error {
	var initErr error
	dc.once.Do(func() {
		opts := []*rpcclntopts.RPCClntOption{
			sprpcclnt.WithSPChannel(dc.fsl, true),
			sprpcclnt.WithDelegatedSPProxyChannel(dc.fsl),
			rpcclntopts.WithShmem(dc.fsl.GetShmemSegment()),
		}
		dc.rpcc, initErr = rpcclnt.NewRPCClnt("", opts...)
	})
	return initErr
}

// GetBytes fetches the delegated RPC reply stored at rpcIdx in SPProxy and
// returns the raw blob bytes. In the shmem path the returned slice is a direct
// view into shmem (no copy); in the non-shmem path it is a heap-allocated
// buffer received over the wire.
func (dc *Clnt) GetBytes(rpcIdx uint64) ([]byte, error) {
	if err := dc.init(); err != nil {
		return nil, err
	}
	res := &rpcproto.GenericBlobRep{
		Blob: &rpcproto.Blob{Iov: [][]byte{{}}},
	}
	if _, err := dc.rpcc.DelegatedRPC(rpcIdx, res); err != nil {
		return nil, err
	}
	return res.Blob.Iov[0], nil
}

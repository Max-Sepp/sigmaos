package clnt

import (
	"time"

	db "sigmaos/debug"
	"sigmaos/proxy/ux/proto"
	rpcclnt "sigmaos/rpc/clnt"
	rpcclntopts "sigmaos/rpc/clnt/opts"
	sprpcclnt "sigmaos/rpc/clnt/sigmap"
	rpcproto "sigmaos/rpc/proto"
	"sigmaos/sigmaclnt/fslib"
)

type UXClnt struct {
	rpcc *rpcclnt.RPCClnt
}

func NewUXClntInit(fsl *fslib.FsLib, pn string, lazyInit bool) (*UXClnt, error) {
	db.DPrintf(db.UXCLNT, "New UXClnt: %v", pn)
	rpcc, err := rpcclnt.NewRPCClnt(pn, sprpcclnt.WithSPChannel(fsl, lazyInit), sprpcclnt.WithDelegatedSPProxyChannel(fsl), rpcclntopts.WithShmem(fsl.GetShmemSegment()))
	if err != nil {
		return nil, err
	}
	return &UXClnt{
		rpcc: rpcc,
	}, nil
}

func NewUXClnt(fsl *fslib.FsLib, pn string) (*UXClnt, error) {
	return NewUXClntInit(fsl, pn, true)
}

func (clnt *UXClnt) GetFile(path string) ([]byte, error) {
	db.DPrintf(db.UXCLNT, "GetFile path:%v", path)
	b := []byte{}
	var res proto.UXRep
	res.Blob = &rpcproto.Blob{
		Iov: [][]byte{b},
	}
	req := &proto.UXReq{
		Path: path,
	}
	err := clnt.rpcc.RPC("UXRpcAPI.GetFile", req, &res)
	if err != nil {
		db.DPrintf(db.UXCLNT_ERR, "Err GetFile: %v", err)
		db.DPrintf(db.ERROR, "Err GetFile: %v", err)
		return nil, err
	}
	db.DPrintf(db.UXCLNT, "GetFile ok path:%v blob_len:%v", path, len(res.Blob.Iov))
	return res.Blob.Iov[0], nil
}

func (clnt *UXClnt) DelegatedGetFile(rpcIdx uint64) ([]byte, time.Duration, error) {
	db.DPrintf(db.UXCLNT, "DelegatedGetFile(%v)", rpcIdx)
	b := []byte{}
	var res proto.UXRep
	res.Blob = &rpcproto.Blob{
		Iov: [][]byte{b},
	}
	transferDur, err := clnt.rpcc.DelegatedRPC(rpcIdx, &res)
	if err != nil {
		db.DPrintf(db.UXCLNT_ERR, "Err DelegatedGetFile: %v", err)
		db.DPrintf(db.ERROR, "Err DelegatedGetFile: %v", err)
		return nil, 0, err
	}
	db.DPrintf(db.UXCLNT, "DelegatedGetFile(%v) ok blob_len:%v", rpcIdx, len(res.Blob.Iov))
	return res.Blob.Iov[0], transferDur, nil
}

func (clnt *UXClnt) PutFile(path string, b []byte) error {
	db.DPrintf(db.UXCLNT, "PutFile path:%v len:%v", path, len(b))
	var res proto.UXRep
	req := &proto.UXReq{
		Path: path,
		Blob: &rpcproto.Blob{
			Iov: [][]byte{b},
		},
	}
	err := clnt.rpcc.RPC("UXRpcAPI.PutFile", req, &res)
	if err != nil {
		db.DPrintf(db.UXCLNT_ERR, "Err PutFile: %v", err)
		db.DPrintf(db.ERROR, "Err PutFile: %v", err)
		return err
	}
	db.DPrintf(db.UXCLNT, "PutFile ok path:%v len:%v", path, len(b))
	return nil
}

func (clnt *UXClnt) DelegatedPutFile(rpcIdx uint64, path string, b []byte) error {
	db.DPrintf(db.UXCLNT, "DelegatedPutFile path:%v len:%v", path, len(b))
	req := &proto.UXReq{
		Path: path,
		Blob: &rpcproto.Blob{
			Iov: [][]byte{b},
		},
	}
	err := clnt.rpcc.OutgoingDelegatedRPC(rpcIdx, "UXRpcAPI.PutFile", req)
	if err != nil {
		db.DPrintf(db.UXCLNT_ERR, "Err DelegatedPutFile: %v", err)
		db.DPrintf(db.ERROR, "Err DelegatedPutFile: %v", err)
		return err
	}
	db.DPrintf(db.UXCLNT, "DelegatedPutFile ok path:%v len:%v", path, len(b))
	return nil
}

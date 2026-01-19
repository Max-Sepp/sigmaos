package fsux

import (
	"io"
	"os"
	"path/filepath"
	"sync"

	"sigmaos/api/fs"
	db "sigmaos/debug"
	"sigmaos/proxy/ux/proto"
	rpcproto "sigmaos/rpc/proto"
)

type UXRpcAPI struct {
	mu   sync.Mutex
	root string
}

func newUXRpcAPI(root string) *UXRpcAPI {
	return &UXRpcAPI{
		root: root,
	}
}

func (ra *UXRpcAPI) fullPath(path string) string {
	return filepath.Join(ra.root, path)
}

func (ra *UXRpcAPI) GetFile(ctx fs.CtxI, req proto.UXReq, rep *proto.UXRep) error {
	db.DPrintf(db.UX, "GetFile RPC: path:%v", req.Path)

	// Construct full path
	fullPath := ra.fullPath(req.Path)

	// Open file
	file, err := os.Open(fullPath)
	if err != nil {
		db.DPrintf(db.UX_ERR, "Err Open: %v", err)
		db.DPrintf(db.ERROR, "Err Open: %v", err)
		return err
	}
	defer file.Close()

	// Get file info
	info, err := file.Stat()
	if err != nil {
		db.DPrintf(db.UX_ERR, "Err Stat: %v", err)
		db.DPrintf(db.ERROR, "Err Stat: %v", err)
		return err
	}

	nbyte := int(info.Size())
	// Set up the reply IOVec
	rep.Blob = &rpcproto.Blob{
		Iov: [][]byte{make([]byte, nbyte)},
	}

	n, err := io.ReadAtLeast(file, rep.Blob.Iov[0], nbyte)
	if n != nbyte || err != nil {
		db.DPrintf(db.UX_ERR, "Err Read: %v", err)
		db.DPrintf(db.ERROR, "Err Read: %v", err)
		return err
	}

	db.DPrintf(db.UX, "GetFile RPC success: path:%v len:%v", req.Path, len(rep.Blob.Iov[0]))
	rep.OK = true
	return nil
}

func (ra *UXRpcAPI) PutFile(ctx fs.CtxI, req proto.UXReq, rep *proto.UXRep) error {
	db.DPrintf(db.UX, "PutFile RPC: path:%v len:%v", req.Path, len(req.Blob.Iov[0]))

	// Construct full path
	fullPath := ra.fullPath(req.Path)

	// Create parent directories if they don't exist
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		db.DPrintf(db.UX_ERR, "Err MkdirAll: %v", err)
		db.DPrintf(db.ERROR, "Err MkdirAll: %v", err)
		return err
	}

	// Write file
	if err := os.WriteFile(fullPath, req.Blob.Iov[0], 0644); err != nil {
		db.DPrintf(db.UX_ERR, "Err WriteFile: %v", err)
		db.DPrintf(db.ERROR, "Err WriteFile: %v", err)
		return err
	}

	db.DPrintf(db.UX, "PutFile RPC success: path:%v len:%v", req.Path, len(req.Blob.Iov[0]))
	rep.OK = true
	return nil
}

package srv

import (
	"bytes"
	"context"
	"io"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"sigmaos/api/fs"
	db "sigmaos/debug"
	"sigmaos/proxy/s3/proto"
	rpcproto "sigmaos/rpc/proto"
)

type S3RpcAPI struct {
	fss3 *Fss3
}

func newRPCAPI(fss3 *Fss3) *S3RpcAPI {
	return &S3RpcAPI{
		fss3: fss3,
	}
}

func (ra *S3RpcAPI) GetObject(ctx fs.CtxI, req proto.S3Req, rep *proto.S3Rep) error {
	clnt, err1 := ra.fss3.getClient(ctx)
	if err1 != nil {
		db.DPrintf(db.S3_ERR, "Err getClient: %v", err1)
		db.DPrintf(db.ERROR, "Err getClient: %v", err1)
		return err1
	}
	input := &s3.GetObjectInput{
		Bucket: &req.Bucket,
		Key:    &req.Key,
	}
	result, err := clnt.GetObject(context.TODO(), input)
	if err != nil {
		db.DPrintf(db.S3_ERR, "Err GetObject: %v", err)
		db.DPrintf(db.ERROR, "Err GetObject: %v", err)
		return err
	}
	nbyte := int(*result.ContentLength)
	// Set up the reply IOVec
	rep.Blob = &rpcproto.Blob{
		Iov: [][]byte{make([]byte, nbyte)},
	}
	n, err := io.ReadAtLeast(result.Body, rep.Blob.Iov[0], nbyte)
	if n != nbyte || err != nil {
		db.DPrintf(db.S3_ERR, "Err Read: %v", err)
		db.DPrintf(db.ERROR, "Err Read: %v", err)
		return err
	}
	if err := result.Body.Close(); err != nil {
		db.DPrintf(db.S3_ERR, "Err Close: %v", err)
		db.DPrintf(db.ERROR, "Err Close: %v", err)
		return err
	}
	return nil
}

func (ra *S3RpcAPI) PutObject(ctx fs.CtxI, req proto.S3Req, rep *proto.S3Rep) error {
	clnt, err1 := ra.fss3.getClient(ctx)
	if err1 != nil {
		db.DPrintf(db.S3_ERR, "Err getClient: %v", err1)
		db.DPrintf(db.ERROR, "Err getClient: %v", err1)
		return err1
	}
	input := &s3.PutObjectInput{
		Bucket: &req.Bucket,
		Key:    &req.Key,
		Body:   bytes.NewReader(req.Blob.Iov[0]),
	}
	if _, err := clnt.PutObject(context.TODO(), input); err != nil {
		db.DPrintf(db.S3_ERR, "Err PutObject: %v", err)
		db.DPrintf(db.ERROR, "Err PutObject: %v", err)
		return err
	}
	return nil
}

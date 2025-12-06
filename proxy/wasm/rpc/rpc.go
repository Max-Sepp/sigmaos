package rpc

type RPCAPI interface {
	Send(rpcIdx uint64, pn string, method string, b []byte, nOutIOV uint64) error
	Recv(rpcIdx uint64, getData bool) ([]byte, error)
	Forward(rpcIdx uint64, newRPCIdx uint64, pn string, nOutIOV uint64) error
}

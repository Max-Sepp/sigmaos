#include <proxy/ux/clnt.h>

namespace sigmaos {
namespace proxy::ux {

bool Clnt::_l = sigmaos::util::log::init_logger(UXCLNT);
bool Clnt::_l_e = sigmaos::util::log::init_logger(UXCLNT_ERR);

Clnt::Clnt(std::shared_ptr<sigmaos::proxy::sigmap::Clnt> sp_clnt,
           std::string svc_pn)
    : _sp_clnt(sp_clnt) {
  auto chan =
      std::make_shared<sigmaos::rpc::spchannel::Channel>(svc_pn, sp_clnt);
  _rpcc =
      std::make_shared<sigmaos::rpc::Clnt>(chan, sp_clnt->GetSPProxyChannel());
}

std::expected<std::shared_ptr<std::string>, sigmaos::serr::Error> Clnt::GetFile(
    std::string path) {
  log(UXCLNT, "GetFile path:{}", path);
  UXReq req;
  UXRep rep;
  req.set_path(path);
  Blob blob;
  auto s = std::make_shared<std::string>();
  blob.mutable_iov()->AddAllocated(s.get());
  rep.set_allocated_blob(&blob);
  auto res = _rpcc->RPC("UXRpcAPI.GetFile", req, rep);
  if (!res.has_value()) {
    log(UXCLNT_ERR, "Err GetFile: {}", res.error().String());
    return std::unexpected(res.error());
  }
  log(UXCLNT, "GetFile ok path:{} len:{}", path, s->size());
  return s;
}

std::expected<int, sigmaos::serr::Error> Clnt::PutFile(std::string path,
                                                       std::string* data) {
  log(UXCLNT, "PutFile path:{} len:{}", path, data->size());
  UXReq req;
  UXRep rep;
  req.set_path(path);
  Blob blob;
  blob.mutable_iov()->AddAllocated(data);
  req.set_allocated_blob(&blob);
  auto res = _rpcc->RPC("UXRpcAPI.PutFile", req, rep);
  {
    auto _ = req.release_blob()->mutable_iov()->ReleaseLast();
  }
  if (!res.has_value()) {
    log(UXCLNT_ERR, "Err PutFile: {}", res.error().String());
    return std::unexpected(res.error());
  }
  log(UXCLNT, "PutFile ok path:{}", path);
  return 0;
}

}  // namespace proxy::ux
}  // namespace sigmaos

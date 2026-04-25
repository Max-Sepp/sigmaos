#pragma once

#include <proxy/buf/buf.h>
#include <proxy/sigmap/sigmap.h>
#include <proxy/ux/proto/ux.pb.h>
#include <rpc/clnt.h>
#include <rpc/spchannel/spchannel.h>
#include <serr/serr.h>
#include <util/log/log.h>

#include <expected>
#include <memory>
#include <string>

namespace sigmaos {
namespace proxy::ux {

const std::string UXCLNT = "UXCLNT";
const std::string UXCLNT_ERR = UXCLNT + sigmaos::util::log::ERR;

class Clnt {
 public:
  Clnt(std::shared_ptr<sigmaos::proxy::sigmap::Clnt> sp_clnt,
       std::string svc_pn);
  ~Clnt() {}

  std::expected<std::shared_ptr<sigmaos::proxy::buf::DataBuf>,
                sigmaos::serr::Error>
  GetFile(std::string path);

  std::expected<std::shared_ptr<sigmaos::proxy::buf::DataBuf>,
                sigmaos::serr::Error>
  DelegatedGetFile(uint64_t rpc_idx);

  std::expected<int, sigmaos::serr::Error> PutFile(std::string path,
                                                   std::string* data);

 private:
  std::shared_ptr<sigmaos::proxy::sigmap::Clnt> _sp_clnt;
  std::shared_ptr<sigmaos::rpc::Clnt> _rpcc;

  static bool _l;
  static bool _l_e;
};

}  // namespace proxy::ux
}  // namespace sigmaos

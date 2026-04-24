#pragma once

#include <proxy/s3/proto/s3.pb.h>
#include <proxy/sigmap/sigmap.h>
#include <rpc/clnt.h>
#include <rpc/spchannel/spchannel.h>
#include <serr/serr.h>
#include <util/log/log.h>

#include <expected>
#include <memory>
#include <string>

namespace sigmaos {
namespace proxy::s3 {

const std::string S3CLNT = "S3CLNT";
const std::string S3CLNT_ERR = S3CLNT + sigmaos::util::log::ERR;

class Clnt {
 public:
  Clnt(std::shared_ptr<sigmaos::proxy::sigmap::Clnt> sp_clnt,
       std::string svc_pn);
  ~Clnt() {}

  std::expected<std::shared_ptr<std::string>, sigmaos::serr::Error> GetObject(
      std::string bucket, std::string key, bool cache);

  std::expected<int, sigmaos::serr::Error> PutObject(std::string bucket,
                                                      std::string key,
                                                      std::string* data);

 private:
  std::shared_ptr<sigmaos::proxy::sigmap::Clnt> _sp_clnt;
  std::shared_ptr<sigmaos::rpc::Clnt> _rpcc;

  static bool _l;
  static bool _l_e;
};

}  // namespace proxy::s3
}  // namespace sigmaos

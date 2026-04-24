#include <proc/status.h>
#include <proxy/s3/clnt.h>
#include <proxy/sigmap/python/sigmaos_c.h>
#include <proxy/sigmap/sigmap.h>
#include <proxy/ux/clnt.h>

#include <cstring>
#include <memory>
#include <string>

struct SigmaosClntState {
  std::shared_ptr<sigmaos::proxy::sigmap::Clnt> sp;
  std::shared_ptr<sigmaos::proxy::s3::Clnt> s3;
  std::shared_ptr<sigmaos::proxy::ux::Clnt> ux;
};

static SigmaosClntState* state(SigmaosClnt clnt) {
  return static_cast<SigmaosClntState*>(clnt);
}

// Thread-local error storage
static thread_local std::string tl_last_error;

static void set_error(const std::string& msg) { tl_last_error = msg; }

static void clear_error() { tl_last_error.clear(); }

static const std::string S3_SVC_BASE = "name/s3/";
static const std::string UX_SVC_BASE = "name/ux/";

static sigmaos::proxy::s3::Clnt* s3_clnt(SigmaosClntState* st) {
  if (!st->s3) {
    std::string kid = st->sp->ProcEnv()->GetKernelID();
    st->s3 =
        std::make_shared<sigmaos::proxy::s3::Clnt>(st->sp, S3_SVC_BASE + kid);
  }
  return st->s3.get();
}

static sigmaos::proxy::ux::Clnt* ux_clnt(SigmaosClntState* st) {
  if (!st->ux) {
    std::string kid = st->sp->ProcEnv()->GetKernelID();
    st->ux =
        std::make_shared<sigmaos::proxy::ux::Clnt>(st->sp, UX_SVC_BASE + kid);
  }
  return st->ux.get();
}

extern "C" {

SigmaosClnt sigmaos_new_clnt() {
  clear_error();
  try {
    auto* st = new SigmaosClntState();
    st->sp = std::make_shared<sigmaos::proxy::sigmap::Clnt>();
    return st;
  } catch (const std::exception& e) {
    set_error(e.what());
    return nullptr;
  }
}

void sigmaos_free_clnt(SigmaosClnt clnt) { delete state(clnt); }

int sigmaos_started(SigmaosClnt clnt) {
  clear_error();
  auto res = state(clnt)->sp->Started();
  if (!res.has_value()) {
    set_error(res.error().String());
    return -1;
  }
  return 0;
}

int sigmaos_exited(SigmaosClnt clnt, int status, const char* msg) {
  clear_error();
  std::string msg_str(msg ? msg : "");
  auto tstatus = static_cast<sigmaos::proc::Tstatus>(status);
  auto res = state(clnt)->sp->Exited(tstatus, msg_str);
  if (!res.has_value()) {
    set_error(res.error().String());
    return -1;
  }
  return 0;
}

char* sigmaos_get_file(SigmaosClnt clnt, const char* pn, size_t* out_len) {
  clear_error();
  auto res = state(clnt)->sp->GetFile(std::string(pn));
  if (!res.has_value()) {
    set_error(res.error().String());
    *out_len = 0;
    return nullptr;
  }
  auto& s = res.value();
  *out_len = s->size();
  char* buf = static_cast<char*>(malloc(s->size()));
  if (buf) {
    memcpy(buf, s->data(), s->size());
  }
  return buf;
}

void sigmaos_free_buf(char* buf) { free(buf); }

int sigmaos_put_file(SigmaosClnt clnt, const char* pn, unsigned int perm,
                     unsigned int mode, const char* data, size_t len) {
  clear_error();
  std::string data_str(data, len);
  auto res = state(clnt)->sp->PutFile(
      std::string(pn), static_cast<sigmaos::sigmap::types::Tperm>(perm),
      static_cast<sigmaos::sigmap::types::Tmode>(mode), &data_str, 0, 0);
  if (!res.has_value()) {
    set_error(res.error().String());
    return -1;
  }
  return static_cast<int>(res.value());
}

char* sigmaos_s3_get_object(SigmaosClnt clnt, const char* bucket,
                            const char* key, int cache, size_t* out_len) {
  clear_error();
  auto res = s3_clnt(state(clnt))
                 ->GetObject(std::string(bucket), std::string(key), cache != 0);
  if (!res.has_value()) {
    set_error(res.error().String());
    *out_len = 0;
    return nullptr;
  }
  auto& s = res.value();
  *out_len = s->size();
  char* buf = static_cast<char*>(malloc(s->size()));
  if (buf) {
    memcpy(buf, s->data(), s->size());
  }
  return buf;
}

int sigmaos_s3_put_object(SigmaosClnt clnt, const char* bucket, const char* key,
                          const char* data, size_t len) {
  clear_error();
  std::string data_str(data, len);
  auto res = s3_clnt(state(clnt))
                 ->PutObject(std::string(bucket), std::string(key), &data_str);
  if (!res.has_value()) {
    set_error(res.error().String());
    return -1;
  }
  return 0;
}

char* sigmaos_ux_get_file(SigmaosClnt clnt, const char* path, size_t* out_len) {
  clear_error();
  auto res = ux_clnt(state(clnt))->GetFile(std::string(path));
  if (!res.has_value()) {
    set_error(res.error().String());
    *out_len = 0;
    return nullptr;
  }
  auto& s = res.value();
  *out_len = s->size();
  char* buf = static_cast<char*>(malloc(s->size()));
  if (buf) {
    memcpy(buf, s->data(), s->size());
  }
  return buf;
}

int sigmaos_ux_put_file(SigmaosClnt clnt, const char* path, const char* data,
                        size_t len) {
  clear_error();
  std::string data_str(data, len);
  auto res = ux_clnt(state(clnt))->PutFile(std::string(path), &data_str);
  if (!res.has_value()) {
    set_error(res.error().String());
    return -1;
  }
  return 0;
}

const char* sigmaos_last_error() { return tl_last_error.c_str(); }

}  // extern "C"

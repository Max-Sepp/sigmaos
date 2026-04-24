#include <proxy/sigmap/python/sigmaos_c.h>
#include <proxy/sigmap/sigmap.h>
#include <proc/status.h>

#include <cstring>
#include <string>

using Clnt = sigmaos::proxy::sigmap::Clnt;

// Thread-local error storage
static thread_local std::string tl_last_error;

static void set_error(const std::string& msg) {
    tl_last_error = msg;
}

static void clear_error() {
    tl_last_error.clear();
}

extern "C" {

SigmaosClnt sigmaos_new_clnt() {
    clear_error();
    try {
        return new Clnt();
    } catch (const std::exception& e) {
        set_error(e.what());
        return nullptr;
    }
}

void sigmaos_free_clnt(SigmaosClnt clnt) {
    delete static_cast<Clnt*>(clnt);
}

int sigmaos_started(SigmaosClnt clnt) {
    clear_error();
    auto res = static_cast<Clnt*>(clnt)->Started();
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
    auto res = static_cast<Clnt*>(clnt)->Exited(tstatus, msg_str);
    if (!res.has_value()) {
        set_error(res.error().String());
        return -1;
    }
    return 0;
}

char* sigmaos_get_file(SigmaosClnt clnt, const char* pn, size_t* out_len) {
    clear_error();
    auto res = static_cast<Clnt*>(clnt)->GetFile(std::string(pn));
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

void sigmaos_free_buf(char* buf) {
    free(buf);
}

int sigmaos_put_file(SigmaosClnt clnt, const char* pn,
                     unsigned int perm, unsigned int mode,
                     const char* data, size_t len) {
    clear_error();
    std::string data_str(data, len);
    auto res = static_cast<Clnt*>(clnt)->PutFile(
        std::string(pn),
        static_cast<sigmaos::sigmap::types::Tperm>(perm),
        static_cast<sigmaos::sigmap::types::Tmode>(mode),
        &data_str, 0, 0);
    if (!res.has_value()) {
        set_error(res.error().String());
        return -1;
    }
    return static_cast<int>(res.value());
}

const char* sigmaos_last_error() {
    return tl_last_error.c_str();
}

} // extern "C"

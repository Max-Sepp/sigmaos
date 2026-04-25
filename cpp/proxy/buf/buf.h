#pragma once

#include <memory>
#include <string>
#include <string_view>

namespace sigmaos {
namespace proxy::buf {

// DataBuf is a unified proxy buffer for S3/UX get results, covering all three
// production paths with a single type:
//
//   - shmem delegated path: holds a string_view into the shmem region. The
//     shmem allocator never reclaims a region while the proc is alive, so no
//     ownership is needed.
//   - heap delegated path: holds a shared_ptr<string> that owns the reply
//     buffer. Multiple DataBufs can coexist simultaneously; each keeps its own
//     buffer alive independently.
//   - copied (non-delegated) path: same as the heap path — wraps the
//     shared_ptr<string> returned by a regular RPC.
//
// In all cases data() and size() return the payload pointer and length.
class DataBuf {
 public:
  // shmem path: view into shmem (no ownership needed)
  explicit DataBuf(std::string_view sv) : _sv(sv), _owned(nullptr) {}

  // heap and copied paths: takes ownership of the buffer
  explicit DataBuf(std::shared_ptr<std::string> owned)
      : _sv(owned->data(), owned->size()), _owned(std::move(owned)) {}

  const char* data() const { return _sv.data(); }
  size_t size() const { return _sv.size(); }
  std::string_view view() const { return _sv; }

 private:
  std::string_view _sv;
  std::shared_ptr<std::string> _owned;
};

}  // namespace proxy::buf
}  // namespace sigmaos

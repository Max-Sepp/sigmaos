import ctypes
import os

_lib = ctypes.CDLL("/usr/local/lib/libsigmaos_py.so")

# sigmaos_new_clnt / sigmaos_free_clnt
_lib.sigmaos_new_clnt.restype = ctypes.c_void_p
_lib.sigmaos_new_clnt.argtypes = []

_lib.sigmaos_free_clnt.restype = None
_lib.sigmaos_free_clnt.argtypes = [ctypes.c_void_p]

# sigmaos_started
_lib.sigmaos_started.restype = ctypes.c_int
_lib.sigmaos_started.argtypes = [ctypes.c_void_p]

# sigmaos_exited
_lib.sigmaos_exited.restype = ctypes.c_int
_lib.sigmaos_exited.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.c_char_p]

# sigmaos_get_file
_lib.sigmaos_get_file.restype = ctypes.c_void_p
_lib.sigmaos_get_file.argtypes = [ctypes.c_void_p, ctypes.c_char_p,
                                   ctypes.POINTER(ctypes.c_size_t)]

# sigmaos_free_buf
_lib.sigmaos_free_buf.restype = None
_lib.sigmaos_free_buf.argtypes = [ctypes.c_void_p]

# sigmaos_put_file
_lib.sigmaos_put_file.restype = ctypes.c_int
_lib.sigmaos_put_file.argtypes = [ctypes.c_void_p, ctypes.c_char_p,
                                   ctypes.c_uint, ctypes.c_uint,
                                   ctypes.c_char_p, ctypes.c_size_t]

# sigmaos_s3_get_object
_lib.sigmaos_s3_get_object.restype = ctypes.c_void_p
_lib.sigmaos_s3_get_object.argtypes = [ctypes.c_void_p, ctypes.c_char_p,
                                        ctypes.c_char_p, ctypes.c_int,
                                        ctypes.POINTER(ctypes.c_size_t)]

# sigmaos_s3_put_object
_lib.sigmaos_s3_put_object.restype = ctypes.c_int
_lib.sigmaos_s3_put_object.argtypes = [ctypes.c_void_p, ctypes.c_char_p,
                                        ctypes.c_char_p, ctypes.c_char_p,
                                        ctypes.c_size_t]

# sigmaos_ux_get_file
_lib.sigmaos_ux_get_file.restype = ctypes.c_void_p
_lib.sigmaos_ux_get_file.argtypes = [ctypes.c_void_p, ctypes.c_char_p,
                                      ctypes.POINTER(ctypes.c_size_t)]

# sigmaos_ux_put_file
_lib.sigmaos_ux_put_file.restype = ctypes.c_int
_lib.sigmaos_ux_put_file.argtypes = [ctypes.c_void_p, ctypes.c_char_p,
                                      ctypes.c_char_p, ctypes.c_size_t]

# sigmaos_last_error
_lib.sigmaos_last_error.restype = ctypes.c_char_p
_lib.sigmaos_last_error.argtypes = []

STATUS_OK      = 1
STATUS_EVICTED = 2
STATUS_ERR     = 3
STATUS_FATAL   = 4


def _last_error():
    return _lib.sigmaos_last_error().decode("utf-8")


class SigmaosClnt:
    def __init__(self):
        self._clnt = _lib.sigmaos_new_clnt()
        if not self._clnt:
            raise RuntimeError(f"sigmaos_new_clnt failed: {_last_error()}")

    def __del__(self):
        if self._clnt:
            _lib.sigmaos_free_clnt(self._clnt)
            self._clnt = None

    def started(self):
        rc = _lib.sigmaos_started(self._clnt)
        if rc != 0:
            raise RuntimeError(f"sigmaos_started failed: {_last_error()}")

    def exited(self, status=STATUS_OK, msg=""):
        rc = _lib.sigmaos_exited(self._clnt, status, msg.encode("utf-8"))
        if rc != 0:
            raise RuntimeError(f"sigmaos_exited failed: {_last_error()}")

    def get_file(self, pn: str) -> bytes:
        out_len = ctypes.c_size_t(0)
        ptr = _lib.sigmaos_get_file(self._clnt, pn.encode("utf-8"),
                                    ctypes.byref(out_len))
        if not ptr:
            raise RuntimeError(f"sigmaos_get_file({pn!r}) failed: {_last_error()}")
        try:
            return bytes(ctypes.cast(ptr, ctypes.POINTER(ctypes.c_char * out_len.value)).contents)
        finally:
            _lib.sigmaos_free_buf(ptr)

    def s3_get_object(self, bucket: str, key: str, cache: bool = False) -> bytes:
        out_len = ctypes.c_size_t(0)
        ptr = _lib.sigmaos_s3_get_object(self._clnt, bucket.encode("utf-8"),
                                          key.encode("utf-8"), int(cache),
                                          ctypes.byref(out_len))
        if not ptr:
            raise RuntimeError(f"sigmaos_s3_get_object({bucket!r}, {key!r}) failed: {_last_error()}")
        try:
            return bytes(ctypes.cast(ptr, ctypes.POINTER(ctypes.c_char * out_len.value)).contents)
        finally:
            _lib.sigmaos_free_buf(ptr)

    def s3_put_object(self, bucket: str, key: str, data: bytes) -> None:
        rc = _lib.sigmaos_s3_put_object(self._clnt, bucket.encode("utf-8"),
                                         key.encode("utf-8"), data, len(data))
        if rc != 0:
            raise RuntimeError(f"sigmaos_s3_put_object({bucket!r}, {key!r}) failed: {_last_error()}")

    def ux_get_file(self, path: str) -> bytes:
        out_len = ctypes.c_size_t(0)
        ptr = _lib.sigmaos_ux_get_file(self._clnt, path.encode("utf-8"),
                                        ctypes.byref(out_len))
        if not ptr:
            raise RuntimeError(f"sigmaos_ux_get_file({path!r}) failed: {_last_error()}")
        try:
            return bytes(ctypes.cast(ptr, ctypes.POINTER(ctypes.c_char * out_len.value)).contents)
        finally:
            _lib.sigmaos_free_buf(ptr)

    def ux_put_file(self, path: str, data: bytes) -> None:
        rc = _lib.sigmaos_ux_put_file(self._clnt, path.encode("utf-8"),
                                       data, len(data))
        if rc != 0:
            raise RuntimeError(f"sigmaos_ux_put_file({path!r}) failed: {_last_error()}")

    def put_file(self, pn: str, data: bytes,
                 perm: int = 0o777, mode: int = 0) -> int:
        rc = _lib.sigmaos_put_file(self._clnt, pn.encode("utf-8"),
                                   perm, mode,
                                   data, len(data))
        if rc < 0:
            raise RuntimeError(f"sigmaos_put_file({pn!r}) failed: {_last_error()}")
        return rc

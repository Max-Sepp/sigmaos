use std::mem;
use std::os::raw::c_char;

pub const EXIT_STATUS_OK: u64 = 0;
pub const EXIT_STATUS_ERR: u64 = 1;
pub const EXIT_STATUS_ABORT_LAUNCH: u64 = 2;
pub const EXIT_MSG_OK: &str = "EXIT_OK";
pub const EXIT_MSG_ABORT_LAUNCH: &str = "EXIT_ABORT_LAUNCH";

mod sigmaos_host {
    #[link(wasm_import_module = "sigmaos_host")]
    unsafe extern "C" {
        pub fn send_rpc(rpc_idx: u64, pn_len: u64, method_len: u64, rpc_len: u64, n_outiov: u64);
        pub fn recv_rpc(rpc_idx: u64, get_data: u64) -> u64;
        pub fn forward_rpc(rpc_idx: u64, new_rpc_idx: u64, pn_len: u64, n_outiov: u64);
        pub fn exit(status: u64, msg_len: u64);
    }
}

pub fn send_rpc(
    buf: &mut [u8],
    rpc_idx: u64,
    pn: &str,
    method: &str,
    rpc_bytes: &[u8],
    n_outiov: u64,
) {
    let mut idx = 0;
    let pn_len = pn.len() as u64;
    for c in pn.bytes() {
        buf[idx] = c;
        idx += 1;
    }
    let mut method_len: u64 = 0;
    for c in method.bytes() {
        buf[idx] = c;
        idx += 1;
        method_len += 1;
    }
    let rpc_len = rpc_bytes.len() as u64;
    for b in rpc_bytes {
        buf[idx] = *b;
        idx += 1;
    }
    unsafe {
        sigmaos_host::send_rpc(rpc_idx, pn_len, method_len, rpc_len, n_outiov);
    }
}

// Returns a tuple of (offsets, lengths)
pub fn recv_rpc(buf: &mut [u8], rpc_idx: u64, get_data: bool) -> (Vec<usize>, Vec<usize>) {
    let n_buf = unsafe { sigmaos_host::recv_rpc(rpc_idx, get_data as u64) };
    let mut buf_lens = Vec::with_capacity(n_buf.try_into().unwrap());
    let mut buf_offs = Vec::with_capacity(n_buf.try_into().unwrap());
    let mut idx: usize = 0;
    for _ in 0..n_buf {
        // Length includes the 8 bytes required to encode len itself
        let len = u64::from_le_bytes(buf[idx..idx + 8].try_into().unwrap()) as usize;
        buf_lens.push(len - 8);
        // Offset it current index + 8 (skip buf length u64)
        buf_offs.push(idx + 8);
        idx += len;
    }
    return (buf_offs, buf_lens);
}

pub fn forward_rpc(buf: &mut [u8], rpc_idx: u64, new_rpc_idx: u64, pn: &str, n_outiov: u64) {
    let mut idx = 0;
    let pn_len = pn.len() as u64;
    for c in pn.bytes() {
        buf[idx] = c;
        idx += 1;
    }
    unsafe { sigmaos_host::forward_rpc(rpc_idx, new_rpc_idx, pn_len, n_outiov) }
}

pub fn exit(buf: &mut [u8], status: u64, msg: &str) {
    let mut idx = 0;
    let msg_len = msg.len() as u64;
    for c in msg.bytes() {
        buf[idx] = c;
        idx += 1;
    }
    unsafe { sigmaos_host::exit(status, msg_len) }
}

#[unsafe(export_name = "allocate")]
pub fn allocate(size: usize) -> *mut c_char {
    let mut buffer = Vec::with_capacity(size);
    let pointer = buffer.as_mut_ptr();
    mem::forget(buffer);
    pointer as *mut c_char
}

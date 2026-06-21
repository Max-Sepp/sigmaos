use sigmaos;
use std::os::raw::c_char;
use std::slice;

#[export_name = "boot"]
pub fn boot(b: *mut c_char, buf_sz: usize) {
    let buf: &mut [u8] = unsafe { slice::from_raw_parts_mut(b as *mut u8, buf_sz) };
    let path_len: usize = u32::from_le_bytes(buf[0..4].try_into().unwrap())
        .try_into()
        .unwrap();
    let kid_len: usize = u32::from_le_bytes(buf[4..8].try_into().unwrap())
        .try_into()
        .unwrap();
    let mut off: usize = 8;
    let path = str::from_utf8(&buf[off..off + path_len]).unwrap();
    off += path_len;
    let kid = str::from_utf8(&buf[off..off + kid_len]).unwrap();
    let rpc_bytes = encode_ux_req(path);
    let pn = "name/ux/".to_owned() + kid;
    sigmaos::send_rpc(buf, 0, &pn, "UXRpcAPI.GetFile", &rpc_bytes, 2);
    sigmaos::exit(buf, sigmaos::EXIT_STATUS_OK, sigmaos::EXIT_MSG_OK);
}

// Manually encodes UXReq{path: path} as protobuf bytes.
// UXReq.path is field 1, wire type 2 (LEN): tag = 0x0a.
fn encode_ux_req(path: &str) -> Vec<u8> {
    let path_bytes = path.as_bytes();
    let mut out = Vec::with_capacity(2 + path_bytes.len());
    out.push(0x0a);
    let mut remaining = path_bytes.len();
    loop {
        if remaining < 0x80 {
            out.push(remaining as u8);
            break;
        }
        out.push((remaining as u8) | 0x80);
        remaining >>= 7;
    }
    out.extend_from_slice(path_bytes);
    out
}

use proto::s3;
use protobuf::Message;
use sigmaos;
use std::os::raw::c_char;
use std::slice;

#[export_name = "boot"]
pub fn boot(b: *mut c_char, buf_sz: usize) {
    // Convert the shared buffer to a slice
    let buf: &mut [u8] = unsafe { slice::from_raw_parts_mut(b as *mut u8, buf_sz) };
    // Get the input arguments to the boot script
    let bucket_len: usize = u32::from_le_bytes(buf[0..4].try_into().unwrap())
        .try_into()
        .unwrap();
    let key_len: usize = u32::from_le_bytes(buf[4..8].try_into().unwrap())
        .try_into()
        .unwrap();
    let kid_len: usize = u32::from_le_bytes(buf[8..12].try_into().unwrap())
        .try_into()
        .unwrap();
    let mut off: usize = 12;
    let bucket = str::from_utf8(&buf[off..off + bucket_len])
        .unwrap()
        .to_string();
    off += bucket_len;
    let key = str::from_utf8(&buf[off..off + key_len])
        .unwrap()
        .to_string();
    off += key_len;
    let kid = str::from_utf8(&buf[off..off + kid_len]).unwrap();
    let mut get_req = s3::GetReq::new();
    get_req.bucket = bucket.clone();
    get_req.key = key.clone() + ".meta";
    let rpc_bytes = get_req.write_to_bytes().unwrap();
    let pn = "name/s3/".to_owned() + &kid;
    sigmaos::send_rpc(buf, 0, &pn, "S3RpcAPI.GetObject", &rpc_bytes, 2);
    let (buf_offs, buf_lens) = sigmaos::recv_rpc(buf, 0, true);
    let start = buf_offs[1];
    let end = buf_offs[1] + buf_lens[1];
    let blob_bytes = &buf[start..end];
    let metadata_tag = str::from_utf8(blob_bytes).unwrap();
    if str::trim(metadata_tag) == "abort" {
        // If metadata tag says to abort launch,bail out
        sigmaos::exit(
            buf,
            sigmaos::EXIT_STATUS_ABORT_LAUNCH,
            &(sigmaos::EXIT_MSG_ABORT_LAUNCH.to_owned() + ": metadata tag abort"),
        );
    } else {
        let mut get_req = s3::GetReq::new();
        get_req.bucket = bucket;
        get_req.key = key;
        let rpc_bytes = get_req.write_to_bytes().unwrap();
        sigmaos::send_rpc(buf, 1, &pn, "S3RpcAPI.GetObject", &rpc_bytes, 2);
        sigmaos::exit(buf, sigmaos::EXIT_STATUS_OK, sigmaos::EXIT_MSG_OK);
    }
}

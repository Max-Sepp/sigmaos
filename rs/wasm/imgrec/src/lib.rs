use getrandom::register_custom_getrandom;
use image::imageops::FilterType;
use proto::s3;
use protobuf::Message;
use std::io::Cursor;
use std::os::raw::c_char;
use std::slice;
use tract_onnx::prelude::*;

// Stub: tract pulls in getrandom transitively but inference needs no entropy.
fn getrandom_stub(buf: &mut [u8]) -> Result<(), getrandom::Error> {
    buf.fill(0);
    Ok(())
}
register_custom_getrandom!(getrandom_stub);

// MobileNetV2 ONNX expects NCHW input with ImageNet normalization.
const MEAN: [f32; 3] = [0.485, 0.456, 0.406];
const STD: [f32; 3] = [0.229, 0.224, 0.225];
const INPUT_DIM: usize = 224;

// Input buffer layout (5 u32 LE lengths, then 5 strings):
//   [0..4]   img_bucket_len   (u32 LE)
//   [4..8]   img_key_len      (u32 LE)
//   [8..12]  model_bucket_len (u32 LE)
//   [12..16] model_key_len    (u32 LE)
//   [16..20] kid_len          (u32 LE)
//   followed by: img_bucket, img_key, model_bucket, model_key, kid
//
// Output: exit msg "class_idx,score"
#[export_name = "boot"]
pub fn boot(b: *mut c_char, buf_sz: usize) {
    let buf: &mut [u8] = unsafe { slice::from_raw_parts_mut(b as *mut u8, buf_sz) };

    // Parse lengths.
    let img_bucket_len = u32::from_le_bytes(buf[0..4].try_into().unwrap()) as usize;
    let img_key_len = u32::from_le_bytes(buf[4..8].try_into().unwrap()) as usize;
    let model_bucket_len = u32::from_le_bytes(buf[8..12].try_into().unwrap()) as usize;
    let model_key_len = u32::from_le_bytes(buf[12..16].try_into().unwrap()) as usize;
    let kid_len = u32::from_le_bytes(buf[16..20].try_into().unwrap()) as usize;

    // Parse strings — copy to owned before any send_rpc overwrites the buffer.
    let mut off = 20;
    let img_bucket = str::from_utf8(&buf[off..off + img_bucket_len])
        .unwrap()
        .to_string();
    off += img_bucket_len;
    let img_key = str::from_utf8(&buf[off..off + img_key_len])
        .unwrap()
        .to_string();
    off += img_key_len;
    let model_bucket = str::from_utf8(&buf[off..off + model_bucket_len])
        .unwrap()
        .to_string();
    off += model_bucket_len;
    let model_key = str::from_utf8(&buf[off..off + model_key_len])
        .unwrap()
        .to_string();
    off += model_key_len;
    let kid = str::from_utf8(&buf[off..off + kid_len])
        .unwrap()
        .to_string();

    let pn = "name/s3/".to_owned() + &kid;

    let transfer_start_us = sigmaos::get_time_us();
    let (model_bytes, img_bytes) = if sigmaos::get_run_co_sandbox() {
        // Delegated path: boot script has already fetched model (rpcIdx=0) and
        // image (rpcIdx=1) into SPProxy's delegated RPC store. Use
        // recv_delegated_rpc which returns a single raw frame at buf_offs[0].
        let (buf_offs, buf_lens) = sigmaos::recv_delegated_rpc(buf, 0);
        let model_bytes: Vec<u8> = buf[buf_offs[0]..buf_offs[0] + buf_lens[0]].to_vec();
        let (buf_offs, buf_lens) = sigmaos::recv_delegated_rpc(buf, 1);
        let img_bytes: Vec<u8> = buf[buf_offs[0]..buf_offs[0] + buf_lens[0]].to_vec();
        sigmaos::log_spawn_latency(
            buf,
            "Paper.Initialization.TransferState",
            sigmaos::get_time_us() - transfer_start_us,
        );
        (model_bytes, img_bytes)
    } else {
        // Direct path: fetch model then image via S3 RPC.
        // model at rpcIdx=0, image at rpcIdx=1.
        let mut req = s3::GetReq::new();
        req.bucket = model_bucket;
        req.key = model_key;
        sigmaos::send_rpc(
            buf,
            0,
            &pn,
            "S3RpcAPI.GetObject",
            &req.write_to_bytes().unwrap(),
            2,
        );
        let (buf_offs, buf_lens) = sigmaos::recv_rpc(buf, 0, true);
        let model_bytes: Vec<u8> = buf[buf_offs[1]..buf_offs[1] + buf_lens[1]].to_vec();

        // send_rpc overwrites buf from the front, which is fine because we
        // already copied model bytes above.
        let mut req = s3::GetReq::new();
        req.bucket = img_bucket;
        req.key = img_key;
        sigmaos::send_rpc(
            buf,
            1,
            &pn,
            "S3RpcAPI.GetObject",
            &req.write_to_bytes().unwrap(),
            2,
        );
        let (buf_offs, buf_lens) = sigmaos::recv_rpc(buf, 1, true);
        let img_bytes: Vec<u8> = buf[buf_offs[1]..buf_offs[1] + buf_lens[1]].to_vec();
        sigmaos::log_spawn_latency(
            buf,
            "Paper.Initialization.DownloadState",
            sigmaos::get_time_us() - transfer_start_us,
        );
        (model_bytes, img_bytes)
    };

    let infer_start_us = sigmaos::get_time_us();
    // Decode JPEG and resize to 224x224.
    let img = image::load_from_memory(&img_bytes)
        .unwrap()
        .resize_exact(INPUT_DIM as u32, INPUT_DIM as u32, FilterType::Triangle)
        .to_rgb8();

    // Build NCHW float tensor with ImageNet normalization.
    let n_pixels = INPUT_DIM * INPUT_DIM;
    let mut chw = vec![0f32; 3 * n_pixels];
    for (i, pixel) in img.pixels().enumerate() {
        for c in 0..3 {
            chw[c * n_pixels + i] = (pixel[c] as f32 / 255.0 - MEAN[c]) / STD[c];
        }
    }

    // Load ONNX model and run inference.
    let model = tract_onnx::onnx()
        .model_for_read(&mut Cursor::new(model_bytes))
        .unwrap()
        .with_input_fact(0, f32::fact(&[1usize, 3, INPUT_DIM, INPUT_DIM]).into())
        .unwrap()
        .into_optimized()
        .unwrap()
        .into_runnable()
        .unwrap();

    let input: Tensor = tract_ndarray::Array4::from_shape_vec((1, 3, INPUT_DIM, INPUT_DIM), chw)
        .unwrap()
        .into();
    let outputs = model.run(tvec!(input.into())).unwrap();
    let scores = outputs[0].to_array_view::<f32>().unwrap();
    let scores = scores.as_slice().unwrap();
    sigmaos::log_spawn_latency(
        buf,
        "Paper.Initialization.AppLoadState",
        sigmaos::get_time_us() - infer_start_us,
    );

    let (class_idx, &score) = scores
        .iter()
        .enumerate()
        .max_by(|(_, a), (_, b)| a.partial_cmp(b).unwrap())
        .unwrap();

    let exit_usg = format!("{},{}", class_idx, score);
    sigmaos::exit(buf, sigmaos::EXIT_STATUS_OK, &exit_usg);
}

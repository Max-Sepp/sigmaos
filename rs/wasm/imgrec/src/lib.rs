use getrandom::register_custom_getrandom;
use image::imageops::FilterType;
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

#[export_name = "handler"]
pub fn handler(img_b: *mut c_char, img_sz: usize, model_b: *mut c_char, model_sz: usize) {
    let img_bytes: &[u8] = unsafe { slice::from_raw_parts(img_b as *const u8, img_sz) };
    let model_bytes: &[u8] = unsafe { slice::from_raw_parts(model_b as *const u8, model_sz) };

    // Decode JPEG and resize to 224x224.
    let img = image::load_from_memory(img_bytes)
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

    let (class_idx, &score) = scores
        .iter()
        .enumerate()
        .max_by(|(_, a), (_, b)| a.partial_cmp(b).unwrap())
        .unwrap();

    // Write [class_idx: u32 LE, score: f32 LE] to the front of img_b.
    let out: &mut [u8] = unsafe { slice::from_raw_parts_mut(img_b as *mut u8, img_sz) };
    out[0..4].copy_from_slice(&(class_idx as u32).to_le_bytes());
    out[4..8].copy_from_slice(&score.to_le_bytes());

    sigmaos::exit(out, sigmaos::EXIT_STATUS_OK, sigmaos::EXIT_MSG_OK);
}

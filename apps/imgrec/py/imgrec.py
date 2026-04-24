#!/usr/bin/env python3
"""
SigmaOS image-recognition proc (Python).

Args: img_bucket img_key model_bucket model_key kid

Fetches the image and MobileNetV2 ONNX model from S3 via the SigmaOS API,
runs inference, and exits with "class_idx,score" as the exit message.
"""

import sys
import io
import numpy as np
from PIL import Image
import onnxruntime as ort
import sigmaos

MEAN = np.array([0.485, 0.456, 0.406], dtype=np.float32)
STD  = np.array([0.229, 0.224, 0.225], dtype=np.float32)


def preprocess(img_bytes: bytes) -> np.ndarray:
    img = Image.open(io.BytesIO(img_bytes)).convert("RGB").resize((224, 224))
    arr = np.array(img, dtype=np.float32) / 255.0   # HWC, [0,1]
    arr = (arr - MEAN) / STD                          # ImageNet normalise
    arr = arr.transpose(2, 0, 1)                      # HWC -> CHW
    return arr[np.newaxis, ...]                        # -> NCHW


def main():
    if len(sys.argv) != 6:
        print(f"usage: {sys.argv[0]} img_bucket img_key model_bucket model_key kid",
              file=sys.stderr)
        sys.exit(1)

    img_bucket, img_key, model_bucket, model_key, kid = sys.argv[1:]
    pn_prefix = f"name/s3/{kid}"

    clnt = sigmaos.SigmaosClnt()
    clnt.started()

    img_bytes   = clnt.get_file(f"{pn_prefix}/{img_bucket}/{img_key}")
    model_bytes = clnt.get_file(f"{pn_prefix}/{model_bucket}/{model_key}")

    tensor = preprocess(img_bytes)

    sess = ort.InferenceSession(model_bytes)
    input_name  = sess.get_inputs()[0].name
    output_name = sess.get_outputs()[0].name
    scores = sess.run([output_name], {input_name: tensor})[0][0]

    class_idx = int(np.argmax(scores))
    score     = float(scores[class_idx])

    print(f"Predicted {class_idx},{score}")
    clnt.exited(sigmaos.STATUS_OK, f"{class_idx},{score}")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""
Standalone MobileNetV2 image recognition — no SigmaOS dependencies.

Usage: imgrec_linux.py <model_path> <image_path>
Output: <class_idx>,<score>
"""

import sys
import numpy as np
from PIL import Image
import onnxruntime as ort

MEAN = np.array([0.485, 0.456, 0.406], dtype=np.float32)
STD  = np.array([0.229, 0.224, 0.225], dtype=np.float32)


def preprocess(img_path: str) -> np.ndarray:
    img = Image.open(img_path).convert("RGB").resize((224, 224))
    arr = np.array(img, dtype=np.float32) / 255.0
    arr = (arr - MEAN) / STD
    arr = arr.transpose(2, 0, 1)   # HWC -> CHW
    return arr[np.newaxis, ...]     # -> NCHW


def main():
    if len(sys.argv) != 3:
        print(f"usage: {sys.argv[0]} <model_path> <image_path>", file=sys.stderr)
        sys.exit(1)
    model_path, img_path = sys.argv[1], sys.argv[2]

    sess = ort.InferenceSession(model_path)
    input_name  = sess.get_inputs()[0].name
    output_name = sess.get_outputs()[0].name

    tensor = preprocess(img_path)
    scores = sess.run([output_name], {input_name: tensor})[0][0]

    class_idx = int(np.argmax(scores))
    score     = float(scores[class_idx])
    print(f"{class_idx},{score}")


if __name__ == "__main__":
    main()

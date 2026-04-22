#!/bin/bash

# MobileNetV2 opset-12 from the ONNX Model Zoo.
# Input: NCHW [1, 3, 224, 224], expects ImageNet normalization applied externally.
# Output: [1, 1000] raw logits (no softmax).

MODEL=mobilenetv2-12.onnx
DEST=/tmp/${MODEL}
URL=https://media.githubusercontent.com/media/onnx/models/main/validated/vision/classification/mobilenet/model/${MODEL}

if [ -f "${DEST}" ]; then
    echo "${DEST} already exists, skipping download"
    exit 0
fi

echo "Downloading ${MODEL} to ${DEST}..."
curl -L --fail --progress-bar "${URL}" -o "${DEST}"

echo "Done: ${DEST} ($(du -sh ${DEST} | cut -f1))"

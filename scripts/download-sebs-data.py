#!/usr/bin/env python3
"""
Download SeBS benchmark test data into a local directory.

Usage:
  download-sebs-data.py [--data-dir DIR]

  --data-dir   Destination directory (default: /tmp/sebs-data).
               The directory is removed and recreated on every run.

Populates data for benchmarks that need local input files:
  210.thumbnailer, 220.video-processing, 411.image-recognition,
  504.dna-visualisation

010.sleep, 110.dynamic-html, 120.uploader, and 501-503.graph-* need no
local data and are not handled here.
"""

import argparse
import os
import shutil
import urllib.request

DEFAULT_DATA_DIR = "/tmp/sebs-data"


def download(url, dest, label=""):
    tag = f"[{label}] " if label else ""
    print(f"{tag}downloading {os.path.basename(dest)} ...")
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    with urllib.request.urlopen(req) as resp, open(dest, "wb") as f:
        shutil.copyfileobj(resp, f)
    size_mb = os.path.getsize(dest) / (1024 * 1024)
    print(f"{tag}done ({size_mb:.1f} MB).")


def main():
    parser = argparse.ArgumentParser(description="Download SeBS benchmark test data")
    parser.add_argument("--data-dir", default=DEFAULT_DATA_DIR,
                        help=f"Destination directory (default: {DEFAULT_DATA_DIR})")
    args = parser.parse_args()

    data_dir = args.data_dir
    if os.path.exists(data_dir):
        print(f"Removing existing {data_dir} ...")
        shutil.rmtree(data_dir)
    os.makedirs(data_dir)
    print(f"Created {data_dir}\n")

    # -------------------------------------------------------------------------
    # 210.thumbnailer — one JPEG image
    # -------------------------------------------------------------------------
    bench_dir = os.path.join(data_dir, "210.thumbnailer")
    os.makedirs(bench_dir)
    download(
        "https://commons.wikimedia.org/wiki/Special:FilePath/Jammlich_crop.jpg",
        os.path.join(bench_dir, "test.jpg"),
        "210.thumbnailer",
    )

    # -------------------------------------------------------------------------
    # 220.video-processing — short MP4 (Big Buck Bunny, CC-BY)
    # -------------------------------------------------------------------------
    bench_dir = os.path.join(data_dir, "220.video-processing")
    os.makedirs(bench_dir)
    download(
        "https://download.blender.org/peach/bigbuckbunny_movies/BigBuckBunny_320x180.mp4",
        os.path.join(bench_dir, "city.mp4"),
        "220.video-processing",
    )

    # -------------------------------------------------------------------------
    # 411.image-recognition — ResNet50 weights + one labeled test image
    # -------------------------------------------------------------------------
    model_dir = os.path.join(data_dir, "411.image-recognition", "model")
    resnet_dir = os.path.join(data_dir, "411.image-recognition", "fake-resnet")
    os.makedirs(model_dir)
    os.makedirs(resnet_dir)

    download(
        "https://download.pytorch.org/models/resnet50-19c8e357.pth",
        os.path.join(model_dir, "resnet50-19c8e357.pth"),
        "411.image-recognition",
    )
    img_name = "800px-Porsche_991_silver_IAA.jpg"
    download(
        "https://commons.wikimedia.org/wiki/Special:FilePath/Porsche_991_silver_IAA.jpg",
        os.path.join(resnet_dir, img_name),
        "411.image-recognition",
    )
    with open(os.path.join(resnet_dir, "val_map.txt"), "w") as f:
        f.write(f"{img_name} 817\n")

    # -------------------------------------------------------------------------
    # 504.dna-visualisation — minimal FASTA file (squiggle needs 100+ bases)
    # -------------------------------------------------------------------------
    bench_dir = os.path.join(data_dir, "504.dna-visualisation")
    os.makedirs(bench_dir)
    with open(os.path.join(bench_dir, "test.fasta"), "w") as f:
        f.write(">test_seq\n")
        f.write("ATCG" * 60 + "\n")
    print("[504.dna-visualisation] generated test.fasta.")

    print(f"\nAll done. Data directory: {data_dir}")


if __name__ == "__main__":
    main()

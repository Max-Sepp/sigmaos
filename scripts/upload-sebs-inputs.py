#!/usr/bin/env python3
"""
Seed SeBS test input data into S3 and print per-benchmark event JSON.

For each benchmark, calls generate_input() from its input.py with a
boto3-backed upload_func that places objects at:
  s3://TAG/serverless-benchmarks-input/BENCH_ID/input/N/KEY
  s3://TAG/serverless-benchmarks-input/BENCH_ID/output/N/KEY

Benchmarks with no S3 inputs (010, 110, 501–503) produce their events inline
without uploading anything.

Usage:
  upload-sebs-inputs.py --tag TAG [--benchmarks B1,B2,...] [--data-dir DIR]
                        [--size test|small|large] [--sebs-dir DIR]
"""

import argparse
import importlib.util
import json
import os
import sys

import boto3


SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DEFAULT_SEBS_DIR = os.path.join(SCRIPT_DIR, "..", "apps", "serverless-benchmarks")
DEFAULT_DATA_DIR = "/tmp/sebs-data"

BENCHMARK_PATHS = {
    "010.sleep":             "benchmarks/000.microbenchmarks/010.sleep",
    "110.dynamic-html":      "benchmarks/100.webapps/110.dynamic-html",
    "120.uploader":          "benchmarks/100.webapps/120.uploader",
    "210.thumbnailer":       "benchmarks/200.multimedia/210.thumbnailer",
    "220.video-processing":  "benchmarks/200.multimedia/220.video-processing",
    "411.image-recognition": "benchmarks/400.inference/411.image-recognition",
    "501.graph-pagerank":    "benchmarks/500.scientific/501.graph-pagerank",
    "502.graph-mst":         "benchmarks/500.scientific/502.graph-mst",
    "503.graph-bfs":         "benchmarks/500.scientific/503.graph-bfs",
    "504.dna-visualisation": "benchmarks/500.scientific/504.dna-visualisation",
}

ALL_BENCHMARKS = list(BENCHMARK_PATHS.keys())

INPUT_PREFIX = "serverless-benchmarks-input"


def load_input_module(sebs_dir, bench_id):
    bench_dir = os.path.join(sebs_dir, BENCHMARK_PATHS[bench_id])
    input_path = os.path.join(bench_dir, "input.py")
    if not os.path.isfile(input_path):
        return None
    spec = importlib.util.spec_from_file_location(f"input_{bench_id}", input_path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def make_upload_func(s3, tag, bench_id, input_paths):
    def upload_func(bucket_idx, key, filepath):
        if not os.path.isfile(filepath):
            print(f"  [{bench_id}] warning: skipping missing file {filepath}")
            return
        s3_key = f"{input_paths[bucket_idx]}/{key}"
        s3.upload_file(filepath, tag, s3_key)
        print(f"  uploaded s3://{tag}/{s3_key}")
    return upload_func


def make_s3_paths(tag, bench_id, n_input, n_output):
    base = f"{INPUT_PREFIX}/{bench_id}"
    input_paths = [f"{base}/input/{i}" for i in range(n_input)]
    output_paths = [f"{base}/output/{i}" for i in range(n_output)]
    return input_paths, output_paths


def generate(sebs_dir, bench_id, tag, size, data_dir, s3):
    mod = load_input_module(sebs_dir, bench_id)
    if mod is None:
        print(f"[{bench_id}] no input.py found", file=sys.stderr)
        return None

    n_in, n_out = (0, 0)
    if hasattr(mod, "buckets_count"):
        n_in, n_out = mod.buckets_count()

    input_paths, output_paths = make_s3_paths(tag, bench_id, n_in, n_out)

    upload_func = None
    if n_in > 0 or n_out > 0:
        if data_dir is None:
            print(f"[{bench_id}] warning: benchmark needs data-dir but --data-dir not given",
                  file=sys.stderr)
        bench_data_dir = os.path.join(data_dir, bench_id) if data_dir else None
        upload_func = make_upload_func(s3, tag, bench_id, input_paths)

        bench_dir = os.path.join(sebs_dir, BENCHMARK_PATHS[bench_id])
        event = mod.generate_input(
            bench_data_dir, size, tag, input_paths, output_paths, upload_func, None
        )
    else:
        event = mod.generate_input(
            None, size, tag, input_paths, output_paths, None, None
        )

    return event


def main():
    parser = argparse.ArgumentParser(description="Upload SeBS test inputs to S3")
    parser.add_argument("--tag", required=True, help="S3 bucket name")
    parser.add_argument("--benchmarks", default=",".join(ALL_BENCHMARKS))
    parser.add_argument("--data-dir", default=DEFAULT_DATA_DIR,
                        help=f"Local directory with per-benchmark test assets (default: {DEFAULT_DATA_DIR})")
    parser.add_argument("--size", default="test", choices=["test", "small", "large"])
    parser.add_argument("--sebs-dir", default=DEFAULT_SEBS_DIR)
    args = parser.parse_args()

    benchmarks = [b.strip() for b in args.benchmarks.split(",") if b.strip()]
    s3 = boto3.client("s3")

    events = {}
    for bench_id in benchmarks:
        if bench_id not in BENCHMARK_PATHS:
            print(f"unknown benchmark: {bench_id}", file=sys.stderr)
            continue
        print(f"[{bench_id}] generating input ...")
        event = generate(args.sebs_dir, bench_id, args.tag, args.size, args.data_dir, s3)
        if event is not None:
            events[bench_id] = event
            print(f"[{bench_id}] event: {json.dumps(event)}")

    print("\n--- All events ---")
    print(json.dumps(events, indent=2))


if __name__ == "__main__":
    main()

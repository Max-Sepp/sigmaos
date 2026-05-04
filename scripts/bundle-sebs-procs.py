#!/usr/bin/env python3
"""
Build and upload per-benchmark SeBS bundles to S3.

Each bundle contains:
  deps/         pip-installed packages (from venv)
  _sebsbench/   function.py, __init__.py, storage.py (SigmaOS shim), local files

Usage:
  bundle-sebs-procs.py --tag TAG [--benchmarks B1,B2,...] [--data-dir DIR]
                       [--sebs-dir DIR] [--storage-py PATH]

  --tag         S3 bucket name (required)
  --benchmarks  Comma-separated benchmark IDs (default: all in-scope)
  --data-dir    Directory with large local deps (e.g. ffmpeg binary for 220)
  --sebs-dir    Path to SeBS repo root (default: sibling of this script's dir)
  --storage-py  Path to SigmaOS storage.py shim (default: autodetected)
"""

import argparse
import glob
import os
import shutil
import stat
import subprocess
import sys
import tempfile

import boto3


SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DEFAULT_SEBS_DIR = os.path.join(SCRIPT_DIR, "..", "apps", "serverless-benchmarks")
DEFAULT_STORAGE_PY = os.path.join(SCRIPT_DIR, "..", "apps", "sebs", "python", "storage.py")
DEFAULT_DATA_DIR = "/home/arielck/projects/research/ffmpeg-master-latest-linux64-gpl/bin"

# Benchmark ID -> relative path within SeBS repo
BENCHMARK_PATHS = {
    "010.sleep":             "benchmarks/000.microbenchmarks/010.sleep/python",
    "110.dynamic-html":      "benchmarks/100.webapps/110.dynamic-html/python",
    "120.uploader":          "benchmarks/100.webapps/120.uploader/python",
    "210.thumbnailer":       "benchmarks/200.multimedia/210.thumbnailer/python",
    "220.video-processing":  "benchmarks/200.multimedia/220.video-processing/python",
    "411.image-recognition": "benchmarks/400.inference/411.image-recognition/python",
    "501.graph-pagerank":    "benchmarks/500.scientific/501.graph-pagerank/python",
    "502.graph-mst":         "benchmarks/500.scientific/502.graph-mst/python",
    "503.graph-bfs":         "benchmarks/500.scientific/503.graph-bfs/python",
    "504.dna-visualisation": "benchmarks/500.scientific/504.dna-visualisation/python",
}

ALL_BENCHMARKS = list(BENCHMARK_PATHS.keys())

# Files/dirs to skip when copying from the benchmark python/ directory
SKIP_NAMES = {
    "function.py", "storage.py", "__init__.py",
    "__pycache__", "requirements.txt", "init.sh", "package.sh",
}
SKIP_SUFFIXES = (".pyc", ".pyo")
SKIP_PREFIXES = ("requirements.txt",)


def _skip(name):
    if name in SKIP_NAMES:
        return True
    for sfx in SKIP_SUFFIXES:
        if name.endswith(sfx):
            return True
    for pfx in SKIP_PREFIXES:
        if name.startswith(pfx):
            return True
    return False


def _install_deps(req_path, dest_dir):
    venv_dir = tempfile.mkdtemp(prefix="sebs-venv-")
    try:
        subprocess.run([sys.executable, "-m", "venv", venv_dir], check=True)
        venv_pip = os.path.join(venv_dir, "bin", "pip")
        subprocess.run([venv_pip, "install", "-r", req_path], check=True)
        site_pkgs = glob.glob(os.path.join(venv_dir, "lib", "python*", "site-packages"))
        if not site_pkgs:
            raise RuntimeError("could not locate site-packages in venv")
        shutil.copytree(site_pkgs[0], dest_dir)
    finally:
        shutil.rmtree(venv_dir, ignore_errors=True)


def bundle_benchmark(bench_id, sebs_dir, storage_py, data_dir, tag, s3):
    python_dir = os.path.join(sebs_dir, BENCHMARK_PATHS[bench_id])
    if not os.path.isdir(python_dir):
        print(f"[{bench_id}] error: python dir not found: {python_dir}", file=sys.stderr)
        return False

    work_dir = tempfile.mkdtemp(prefix=f"sebs-bundle-{bench_id}-")
    try:
        deps_dir = os.path.join(work_dir, "deps")
        func_dir = os.path.join(work_dir, "_sebsbench")
        os.makedirs(func_dir)

        # Install pip dependencies
        req_path = os.path.join(python_dir, "requirements.txt")
        if os.path.isfile(req_path):
            with open(req_path) as f:
                lines = [l.strip() for l in f if l.strip() and not l.strip().startswith("#")]
            if lines:
                print(f"[{bench_id}] installing {len(lines)} requirement(s) ...")
                _install_deps(req_path, deps_dir)
            else:
                print(f"[{bench_id}] requirements.txt is empty, skipping pip install")
        else:
            print(f"[{bench_id}] no requirements.txt found")

        # Copy function.py
        shutil.copy2(os.path.join(python_dir, "function.py"), os.path.join(func_dir, "function.py"))

        # Empty __init__.py
        open(os.path.join(func_dir, "__init__.py"), "w").close()

        # SigmaOS storage shim
        shutil.copy2(storage_py, os.path.join(func_dir, "storage.py"))

        # Remaining local files from python/ (e.g. templates/, imagenet_class_index.json)
        for entry in os.listdir(python_dir):
            if _skip(entry):
                continue
            src = os.path.join(python_dir, entry)
            dst = os.path.join(func_dir, entry)
            if os.path.isdir(src):
                shutil.copytree(src, dst)
            else:
                shutil.copy2(src, dst)

        # Benchmark-specific extras from the benchmark directory (one level up from python/)
        bench_dir = os.path.dirname(python_dir)
        if bench_id == "220.video-processing":
            # watermark.png from benchmark dir (already in the repo)
            resources_src = os.path.join(bench_dir, "resources")
            if os.path.isdir(resources_src):
                shutil.copytree(resources_src, os.path.join(func_dir, "resources"))
            # ffmpeg binary from data-dir
            if data_dir:
                ffmpeg_src_dir = os.path.join(data_dir, )
                ffmpeg_bin = os.path.join(ffmpeg_src_dir, "ffmpeg")
                if os.path.isfile(ffmpeg_bin):
                    ffmpeg_dst_dir = os.path.join(func_dir, "ffmpeg")
                    os.makedirs(ffmpeg_dst_dir)
                    dst_bin = os.path.join(ffmpeg_dst_dir, "ffmpeg")
                    shutil.copy2(ffmpeg_bin, dst_bin)
                    # Ensure executable bit is set before tarring
                    st = os.stat(dst_bin)
                    os.chmod(dst_bin, st.st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)
                else:
                    print(f"[{bench_id}] warning: ffmpeg binary not found at {ffmpeg_bin} "
                          f"(run init.sh in a data-dir first, or pass --data-dir)")
            else:
                print(f"[{bench_id}] warning: --data-dir not given; ffmpeg binary will be missing")

        # Uncompressed tarball
        tar_name = f"{bench_id}-bundle.tar"
        tar_path = os.path.join(tempfile.gettempdir(), tar_name)
        print(f"[{bench_id}] creating {tar_path} ...")
        subprocess.run(["tar", "-cf", tar_path, "-C", work_dir, "."], check=True)

        # Compressed tarball
        tarball_name = f"{bench_id}-bundle.tar.gz"
        tarball_path = os.path.join(tempfile.gettempdir(), tarball_name)
        print(f"[{bench_id}] creating {tarball_path} ...")
        subprocess.run(["tar", "-czf", tarball_path, "-C", work_dir, "."], check=True)

        tar_size_mb = os.path.getsize(tar_path) / (1024 * 1024)
        gz_size_mb = os.path.getsize(tarball_path) / (1024 * 1024)
        print(f"[{bench_id}] bundle sizes: uncompressed={tar_size_mb:.1f} MB, compressed={gz_size_mb:.1f} MB")

        # Upload uncompressed
        s3_key_tar = f"bin/{tar_name}"
        print(f"[{bench_id}] uploading to s3://{tag}/{s3_key_tar} ({tar_size_mb:.1f} MB) ...")
        s3.upload_file(tar_path, tag, s3_key_tar)

        # Upload compressed
        s3_key_gz = f"bin/{tarball_name}"
        print(f"[{bench_id}] uploading to s3://{tag}/{s3_key_gz} ({gz_size_mb:.1f} MB) ...")
        s3.upload_file(tarball_path, tag, s3_key_gz)

        print(f"[{bench_id}] done.")
        return True
    finally:
        shutil.rmtree(work_dir, ignore_errors=True)


def main():
    parser = argparse.ArgumentParser(description="Build and upload SeBS proc bundles")
    parser.add_argument("--tag", required=True, help="S3 bucket name")
    parser.add_argument("--benchmark", default=None,
                        help="Single benchmark ID to bundle (overrides --benchmarks)")
    parser.add_argument("--benchmarks", default=",".join(ALL_BENCHMARKS),
                        help="Comma-separated benchmark IDs (default: all in-scope)")
    parser.add_argument("--data-dir", default=DEFAULT_DATA_DIR,
                        help="Directory with large local file deps (e.g. ffmpeg)")
    parser.add_argument("--sebs-dir", default=DEFAULT_SEBS_DIR,
                        help="Path to SeBS repo root")
    parser.add_argument("--storage-py", default=DEFAULT_STORAGE_PY,
                        help="Path to SigmaOS storage.py shim")
    args = parser.parse_args()

    if args.benchmark is not None:
        benchmarks = [args.benchmark.strip()]
    else:
        benchmarks = [b.strip() for b in args.benchmarks.split(",") if b.strip()]
    unknown = [b for b in benchmarks if b not in BENCHMARK_PATHS]
    if unknown:
        print(f"error: unknown benchmarks: {unknown}", file=sys.stderr)
        sys.exit(1)

    if not os.path.isfile(args.storage_py):
        print(f"error: storage.py not found: {args.storage_py}", file=sys.stderr)
        sys.exit(1)

    s3 = boto3.client("s3")
    failed = []
    for bench_id in benchmarks:
        ok = bundle_benchmark(bench_id, args.sebs_dir, args.storage_py, args.data_dir, args.tag, s3)
        if not ok:
            failed.append(bench_id)

    if failed:
        print(f"failed: {failed}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()

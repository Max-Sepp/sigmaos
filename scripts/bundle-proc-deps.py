#!/usr/bin/env python3
"""
Bundle a proc's Python dependencies and upload them to S3.

Usage:
  bundle-proc-deps.py --proc PROC --tag TAG --path PATH

  --path     Directory containing requirements.txt
  --proc     Name of the proc (used in tarball and S3 key)
  --tag      S3 bucket name (also used as the key prefix)
"""

import argparse
import glob
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile

import boto3


def main():
    parser = argparse.ArgumentParser(description="Bundle proc deps and upload to S3")
    parser.add_argument("--path", required=True, help="Directory containing requirements.txt")
    parser.add_argument("--proc", required=True, help="Proc name")
    parser.add_argument("--tag", required=True, help="S3 bucket name and key prefix")
    args = parser.parse_args()

    req_path = os.path.join(args.path, "requirements.txt")
    if not os.path.isfile(req_path):
        print(f"error: no requirements.txt found in {args.path}", file=sys.stderr)
        sys.exit(1)

    bundle_dir = "/tmp/bundle"
    if os.path.exists(bundle_dir):
        shutil.rmtree(bundle_dir)

    # Install into a fresh venv to avoid conflicts with system packages,
    # then copy site-packages to the bundle dir.
    venv_dir = tempfile.mkdtemp(prefix="bundle-venv-")
    try:
        print(f"Creating venv at {venv_dir} ...")
        subprocess.run([sys.executable, "-m", "venv", venv_dir], check=True)
        venv_pip = os.path.join(venv_dir, "bin", "pip")
        print(f"Installing requirements from {req_path} ...")
        subprocess.run([venv_pip, "install", "-r", req_path], check=True)
        site_packages = glob.glob(
            os.path.join(venv_dir, "lib", "python*", "site-packages")
        )
        if not site_packages:
            print("error: could not locate site-packages in venv", file=sys.stderr)
            sys.exit(1)
        shutil.copytree(site_packages[0], bundle_dir)
    finally:
        shutil.rmtree(venv_dir)

    tarball_name = f"{args.proc}-bundle.tar.gz"
    tarball_path = os.path.join(tempfile.gettempdir(), tarball_name)
    print(f"Creating tarball {tarball_path} ...")
    with tarfile.open(tarball_path, "w:gz") as tar:
        tar.add(bundle_dir, arcname=".")

    s3_key = f"bin/{tarball_name}"
    print(f"Uploading to s3://{args.tag}/{s3_key} ...")
    s3 = boto3.client("s3")
    s3.upload_file(tarball_path, args.tag, s3_key)
    size_mb = os.path.getsize(tarball_path) / (1024 * 1024)
    print(f"Done. Bundle size: {size_mb:.1f} MB")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""
SigmaOS SeBS runner proc.

Untars BENCH_ID-bundle.tar.gz from CWD, initializes the SigmaOS storage shim,
and calls handler(event). The bundle is downloaded on behalf of the runner
before it starts.

Args:
  --benchmark   Benchmark ID, e.g. "010.sleep"
  --event       JSON-encoded event dict
  --async-fetch Use async S3 gets (currently a no-op; reserved)
  --delegated   Use delegated S3 gets via co-sandbox shmem
"""

import argparse
import importlib
import json
import os
import subprocess
import sys
import time
import uuid

import sigmaos


class _SafeEncoder(json.JSONEncoder):
    """Encode igraph objects and other non-standard types so json.dumps never crashes."""
    def default(self, obj):
        try:
            import igraph
            if isinstance(obj, igraph.Graph):
                return obj.get_edgelist()
        except ImportError:
            pass
        try:
            return list(obj)
        except TypeError:
            return str(obj)


def main():
    clnt = sigmaos.SigmaosClnt()
    clnt.started()

    parser = argparse.ArgumentParser()
    parser.add_argument("--benchmark", required=True)
    parser.add_argument("--event", required=True)
    parser.add_argument("--async-fetch", action="store_true")
    parser.add_argument("--delegated", action="store_true")
    parser.add_argument("--uncompressed", action="store_true")
    args = parser.parse_args()

    try:
        _run(clnt, args)
    except Exception as e:
        clnt.exited(sigmaos.STATUS_ERR, str(e))
        sys.exit(1)


def _run(clnt, args):
    if args.uncompressed:
        bundle_name = f"{args.benchmark}-bundle.tar"
    else:
        bundle_name = f"{args.benchmark}-bundle.tar.gz"
    bundle_path = os.path.join(os.getcwd(), "bin", "user", bundle_name)

    pkgroot = f"/tmp/sebs-{uuid.uuid4()}"
    os.makedirs(pkgroot, exist_ok=True)

    untar_start = time.perf_counter()
    subprocess.run(["tar", "-xf", bundle_path, "-C", pkgroot], check=True)
    clnt.log_spawn_latency("Paper.Setup.Untar",
                           int((time.perf_counter() - untar_start) * 1_000_000))

    # deps/ contains pip-installed packages; add first so _sebsbench imports work
    deps_dir = os.path.join(pkgroot, "deps")
    if os.path.isdir(deps_dir):
        sys.path.insert(0, deps_dir)

    # pkgroot itself must be on sys.path so "import _sebsbench" resolves
    sys.path.insert(0, pkgroot)

    # Step a: import the storage shim module (already in bundle as _sebsbench/storage.py)
    storage_mod = importlib.import_module("_sebsbench.storage")

    # Step b: create and assign singleton BEFORE importing function module.
    # Several benchmarks call storage.storage.get_instance() at module level.
    storage_mod.storage.instance = storage_mod.storage(clnt, use_delegation=args.delegated)

    # Step c: now safe to import the function module
    func_mod = importlib.import_module("_sebsbench.function")

    event = json.loads(args.event)
    result = func_mod.handler(event)
    clnt.exited(sigmaos.STATUS_OK, json.dumps(result, cls=_SafeEncoder))


if __name__ == "__main__":
    main()

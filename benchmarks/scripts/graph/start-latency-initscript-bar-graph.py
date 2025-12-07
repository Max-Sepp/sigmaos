#!/usr/bin/env python3

import argparse
import glob
import os
import re
import sys
import matplotlib.pyplot as plt
import numpy as np


def find_proc_pid(dir_path, proc_name, start=True):
    """
    Search for log lines matching "Scale proc_name.*" in bench.out.* files
    and extract the proc_pid from the matching line.
    For start mode: uses first matching line
    """
    action = "Scale"
    pattern = f"{action} {re.escape(proc_name)}.*"
    bench_files = glob.glob(os.path.join(dir_path, "bench.out.*"))

    if not bench_files:
        print(f"Error: No bench.out.* files found in {dir_path}", file=sys.stderr)
        return None

    matching_lines = []

    for bench_file in sorted(bench_files):
        try:
            with open(bench_file, 'r') as f:
                for line in f:
                    if re.search(pattern, line):
                        matching_lines.append(line.strip())
        except Exception as e:
            print(f"Warning: Could not read {bench_file}: {e}", file=sys.stderr)

    if start:
        if len(matching_lines) < 1:
            print(f"Error: Found {len(matching_lines)} matching lines for {proc_name} in {dir_path}, need at least 1", file=sys.stderr)
            print(f"Pattern: {pattern}", file=sys.stderr)
            return None
        target_line = matching_lines[0]
    else:
        if len(matching_lines) < 2:
            print(f"Error: Found {len(matching_lines)} matching lines for {proc_name} in {dir_path}, need at least 2", file=sys.stderr)
            print(f"Pattern: {pattern}", file=sys.stderr)
            return None
        target_line = matching_lines[1]

    # Extract the last word as proc_pid
    proc_pid = target_line.split()[-1]

    return proc_pid


def parse_timing_line(line):
    """
    Parse a log line to extract phase, operation name, sinceSpawn, and op timing.
    Expected format: [proc_pid] Setup.OperationName or Initialization.OperationName ... sinceSpawn:123ms ... op:456ms
    Returns tuple of (phase, op_name, since_spawn_ms, op_time_ms) or None if parsing fails
    """
    # Extract the phase (Setup or Initialization) and operation name
    op_match = re.search(r'\] (Setup|Initialization)\.(\S+)', line)
    if not op_match:
        return None

    phase = op_match.group(1)
    op_name = op_match.group(2)

    # Extract sinceSpawn timing
    spawn_match = re.search(r'sinceSpawn:(\d+(?:\.\d+)?)(ms|µs|us|s)', line)
    since_spawn_ms = None
    if spawn_match:
        timing_value = float(spawn_match.group(1))
        timing_unit = spawn_match.group(2)
        # Convert to milliseconds
        if timing_unit in ['µs', 'us']:
            since_spawn_ms = timing_value / 1000.0
        elif timing_unit == 's':
            since_spawn_ms = timing_value * 1000.0
        else:  # ms
            since_spawn_ms = timing_value

    # Extract op timing
    op_match_timing = re.search(r'op:(\d+(?:\.\d+)?)(ms|µs|us|s)', line)
    op_time_ms = None
    if op_match_timing:
        timing_value = float(op_match_timing.group(1))
        timing_unit = op_match_timing.group(2)
        # Convert to milliseconds
        if timing_unit in ['µs', 'us']:
            op_time_ms = timing_value / 1000.0
        elif timing_unit == 's':
            op_time_ms = timing_value * 1000.0
        else:  # ms
            op_time_ms = timing_value

    return (phase, op_name, since_spawn_ms, op_time_ms)


def find_setup_init_lines(dir_path, proc_pid):
    """
    Search all log files in dir_path/sigmaos-node-logs for lines matching
    "[proc_pid] Setup\\.*" or "[proc_pid] Initialization\\.*"
    Returns two dicts: setup_timings and init_timings, each mapping operation names
    to (sinceSpawn, op_time) tuples. If duplicates exist, keeps the last occurrence.
    """
    log_dir = os.path.join(dir_path, "sigmaos-node-logs")

    if not os.path.isdir(log_dir):
        print(f"Error: Directory {log_dir} does not exist", file=sys.stderr)
        return {}, {}

    log_files = glob.glob(os.path.join(log_dir, "*"))

    if not log_files:
        print(f"Error: No log files found in {log_dir}", file=sys.stderr)
        return {}, {}

    # Patterns to match
    setup_pattern = re.compile(rf"\[{re.escape(proc_pid)}\] Setup\..*")
    init_pattern = re.compile(rf"\[{re.escape(proc_pid)}\] Initialization\..*")

    # Use separate dicts for Setup and Initialization timings
    setup_timings = {}
    init_timings = {}

    for log_file in sorted(log_files):
        # Skip directories
        if os.path.isdir(log_file):
            continue

        try:
            with open(log_file, 'r') as f:
                for line in f:
                    line = line.strip()
                    if setup_pattern.search(line) or init_pattern.search(line):
                        parsed = parse_timing_line(line)
                        if parsed:
                            phase, op_name, since_spawn_ms, op_time_ms = parsed
                            if phase == "Setup":
                                setup_timings[op_name] = (since_spawn_ms, op_time_ms)
                            else:  # Initialization
                                init_timings[op_name] = (since_spawn_ms, op_time_ms)
        except Exception as e:
            print(f"Warning: Could not read {log_file}: {e}", file=sys.stderr)

    return setup_timings, init_timings


def get_last_init_time(dir_path, proc_name):
    """
    Extract the time since spawn for the last initialization step for a given proc.
    Returns the max sinceSpawn value from all initialization steps, or None if not found.
    """
    proc_pid = find_proc_pid(dir_path, proc_name, start=True)
    if proc_pid is None:
        return None

    _, init_timings = find_setup_init_lines(dir_path, proc_pid)

    if not init_timings:
        print(f"Warning: No initialization timings found for {proc_name} in {dir_path}", file=sys.stderr)
        return None

    # Find the maximum sinceSpawn value (the last initialization step)
    max_since_spawn = None
    for op_name, (since_spawn_ms, op_time_ms) in init_timings.items():
        if since_spawn_ms is not None:
            if max_since_spawn is None or since_spawn_ms > max_since_spawn:
                max_since_spawn = since_spawn_ms

    return max_since_spawn


def main():
    parser = argparse.ArgumentParser(
        description="Create bar graph comparing proc startup initialization times"
    )
    parser.add_argument(
        "--dir_path_etcd",
        required=True,
        help="Path to etcd benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_etcd_initscript",
        required=True,
        help="Path to etcd with initscript benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_vecdb",
        required=True,
        help="Path to vecdb benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_vecdb_initscript",
        required=True,
        help="Path to vecdb with initscript benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_cached",
        required=True,
        help="Path to cached benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_cached_initscript",
        required=True,
        help="Path to cached with initscript benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_memcached",
        required=True,
        help="Path to memcached benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_memcached_initscript",
        required=True,
        help="Path to memcached with initscript benchmark output directory"
    )
    parser.add_argument(
        "--output",
        default="start-latency-initscript-comparison.png",
        help="Output filename for the graph (default: start-latency-initscript-comparison.png)"
    )

    args = parser.parse_args()

    # Extract data for each proc
    data = {
        'etcd-shim': {
            'without_initscript': get_last_init_time(args.dir_path_etcd, 'etcd-shim'),
            'with_initscript': get_last_init_time(args.dir_path_etcd_initscript, 'etcd-shim')
        },
        'cossim-srv-cpp': {
            'without_initscript': get_last_init_time(args.dir_path_vecdb, 'cossim-srv-cpp'),
            'with_initscript': get_last_init_time(args.dir_path_vecdb_initscript, 'cossim-srv-cpp')
        },
        'cached-srv-cpp': {
            'without_initscript': get_last_init_time(args.dir_path_cached, 'cached-srv-cpp'),
            'with_initscript': get_last_init_time(args.dir_path_cached_initscript, 'cached-srv-cpp')
        },
        'memcached-shim': {
            'without_initscript': get_last_init_time(args.dir_path_memcached, 'memcached-shim'),
            'with_initscript': get_last_init_time(args.dir_path_memcached_initscript, 'memcached-shim')
        }
    }

    # Check if we have any data
    if all(v['without_initscript'] is None and v['with_initscript'] is None for v in data.values()):
        print("Error: No data found for any proc", file=sys.stderr)
        sys.exit(1)

    # Prepare data for plotting
    procs = ['etcd-shim', 'cossim-srv-cpp', 'cached-srv-cpp', 'memcached-shim']
    proc_labels = ['Etcd', 'VecDB', 'Cached', 'Memcached']

    without_initscript = [data[proc]['without_initscript'] if data[proc]['without_initscript'] is not None else 0 for proc in procs]
    with_initscript = [data[proc]['with_initscript'] if data[proc]['with_initscript'] is not None else 0 for proc in procs]

    # Create bar graph
    x = np.arange(len(proc_labels))
    width = 0.35

    fig, ax = plt.subplots(figsize=(6.4, 2.4))
    bars1 = ax.bar(x - width/2, without_initscript, width, label='Without Initscript', color='steelblue')
    bars2 = ax.bar(x + width/2, with_initscript, width, label='With Initscript', color='coral')

    # Customize the plot
    ax.set_xlabel('Service', fontsize=12)
    ax.set_ylabel('Start time (ms)', fontsize=12)
    ax.set_xticks(x)
    ax.set_xticklabels(proc_labels)
    ax.legend()
    ax.grid(axis='y', alpha=0.3, linestyle='--')

    # Add value labels on top of bars
    def add_value_labels(bars):
        for bar in bars:
            height = bar.get_height()
            if height > 0:
                ax.text(bar.get_x() + bar.get_width()/2., height,
                       f'{height:.0f}ms',
                       ha='center', va='bottom', fontsize=9)

    add_value_labels(bars1)
    add_value_labels(bars2)

    # Add headroom at the top for labels
    y_max = max(max(without_initscript), max(with_initscript))
    ax.set_ylim(0, y_max * 1.15)

    plt.tight_layout()
    plt.savefig(args.output, dpi=300, bbox_inches='tight')
    print(f"Graph saved to {args.output}")

    # Print summary statistics
    print("\nSummary:")
    print("=" * 80)
    for i, proc in enumerate(procs):
        without = data[proc]['without_initscript']
        with_init = data[proc]['with_initscript']
        print(f"{proc_labels[i]:15} | Without: {without:.2f}ms | With: {with_init:.2f}ms | Diff: {(without - with_init):.2f}ms ({((without - with_init) / without * 100):.1f}%)" if without and with_init else f"{proc_labels[i]:15} | Data missing")


if __name__ == "__main__":
    main()

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
    Expected format: [proc_pid] [Paper.]Setup.OperationName or [Paper.]Initialization.OperationName ... sinceSpawn:123ms ... op:456ms
    Returns tuple of (phase, op_name, since_spawn_ms, op_time_ms) or None if parsing fails
    """
    # Extract the phase (Setup or Initialization) and operation name.
    # Labels may optionally be prefixed with "Paper." (e.g. "Paper.Initialization.TransferState").
    op_match = re.search(r'\] (?:Paper\.)?(Setup|Initialization)\.(\S+)', line)
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
    "[proc_pid] [Paper.]Setup\\.*" or "[proc_pid] [Paper.]Initialization\\.*"
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

    # Patterns to match — the Paper. prefix is optional
    escaped_pid = re.escape(proc_pid)
    setup_pattern = re.compile(rf"\[{escaped_pid}\] (?:Paper\.)?Setup\..*")
    init_pattern = re.compile(rf"\[{escaped_pid}\] (?:Paper\.)?Initialization\..*")

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


def get_setup_and_init_times(dir_path, proc_name):
    """
    Returns (setup_span_ms, init_start_ms, init_span_ms):
      setup_span  = end of last setup op - start of first setup op (bottom always 0)
      init_start  = start of first init op (sinceSpawn - opTime, clamped to 0)
      init_span   = end of last init op - init_start
    Returns (None, None, None) on failure.
    """
    proc_pid = find_proc_pid(dir_path, proc_name, start=True)
    if proc_pid is None:
        return None, None, None

    setup_timings, init_timings = find_setup_init_lines(dir_path, proc_pid)

    def phase_bounds(timings):
        """Returns (start_ms, end_ms) for a phase, or (None, None)."""
        end_ms   = None
        start_ms = None
        for _, (since_spawn_ms, op_time_ms) in timings.items():
            if since_spawn_ms is None:
                continue
            if end_ms is None or since_spawn_ms > end_ms:
                end_ms = since_spawn_ms
            s = max(0.0, since_spawn_ms - op_time_ms) if op_time_ms is not None else 0.0
            if start_ms is None or s < start_ms:
                start_ms = s
        return start_ms, end_ms

    _, setup_end       = phase_bounds(setup_timings)
    init_start, init_end = phase_bounds(init_timings)

    if setup_end is None and init_end is None:
        return None, None, None

    setup_span = setup_end if setup_end is not None else 0.0
    init_start = init_start or 0.0
    init_span  = (init_end - init_start) if init_end is not None else 0.0

    return setup_span, init_start, init_span


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
        "--dir_path_etcd_cosandbox",
        required=True,
        help="Path to etcd with cosandbox benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_vecdb",
        required=True,
        help="Path to vecdb benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_vecdb_cosandbox",
        required=True,
        help="Path to vecdb with cosandbox benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_cached",
        required=True,
        help="Path to cached benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_cached_cosandbox",
        required=True,
        help="Path to cached with cosandbox benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_memcached",
        required=True,
        help="Path to memcached benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_memcached_cosandbox",
        required=True,
        help="Path to memcached with cosandbox benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_imgrec_wasm",
        required=True,
        help="Path to imgrec-wasm benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_imgrec_wasm_cosandbox",
        required=True,
        help="Path to imgrec-wasm with cosandbox benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_imgrec_py",
        required=True,
        help="Path to imgrec-py benchmark output directory"
    )
    parser.add_argument(
        "--dir_path_imgrec_py_cosandbox",
        required=True,
        help="Path to imgrec-py with cosandbox benchmark output directory"
    )
    parser.add_argument(
        "--output_shims",
        default="start-latency-cosandbox-shims.pdf",
        help="Output PDF for etcd + memcached graph (default: start-latency-cosandbox-shims.pdf)"
    )
    parser.add_argument(
        "--output_cpp",
        default="start-latency-cosandbox-cpp.pdf",
        help="Output PDF for vecdb + cached graph (default: start-latency-cosandbox-cpp.pdf)"
    )
    parser.add_argument(
        "--output_imgrec",
        default="start-latency-cosandbox-imgrec.pdf",
        help="Output PDF for imgrec-wasm + imgrec-py graph (default: start-latency-cosandbox-imgrec.pdf)"
    )
    parser.add_argument(
        "--show_breakdown",
        action="store_true",
        help="Overlay setup and initialization time segments on each bar"
    )
    parser.add_argument(
        "--sys-name",
        default="co-sandbox",
        help="Label to use in place of 'co-sandbox' in legend entries (default: co-sandbox)"
    )

    args = parser.parse_args()

    # Extract data for each proc
    data = {
        'etcd-shim': {
            'without_cosandbox': get_last_init_time(args.dir_path_etcd, 'etcd-shim'),
            'with_cosandbox': get_last_init_time(args.dir_path_etcd_cosandbox, 'etcd-shim')
        },
        'cossim-srv-cpp': {
            'without_cosandbox': get_last_init_time(args.dir_path_vecdb, 'cossim-srv-cpp'),
            'with_cosandbox': get_last_init_time(args.dir_path_vecdb_cosandbox, 'cossim-srv-cpp')
        },
        'cached-srv-cpp': {
            'without_cosandbox': get_last_init_time(args.dir_path_cached, 'cached-srv-cpp'),
            'with_cosandbox': get_last_init_time(args.dir_path_cached_cosandbox, 'cached-srv-cpp')
        },
        'memcached-shim': {
            'without_cosandbox': get_last_init_time(args.dir_path_memcached, 'memcached-shim'),
            'with_cosandbox': get_last_init_time(args.dir_path_memcached_cosandbox, 'memcached-shim')
        },
        'imgrec_precompiled.wasm': {
            'without_cosandbox': get_last_init_time(args.dir_path_imgrec_wasm, 'imgrec_precompiled.wasm'),
            'with_cosandbox': get_last_init_time(args.dir_path_imgrec_wasm_cosandbox, 'imgrec_precompiled.wasm')
        },
        'imgrec.py': {
            'without_cosandbox': get_last_init_time(args.dir_path_imgrec_py, 'imgrec.py'),
            'with_cosandbox': get_last_init_time(args.dir_path_imgrec_py_cosandbox, 'imgrec.py')
        },
    }

    # Check if we have any data
    if all(v['without_cosandbox'] is None and v['with_cosandbox'] is None for v in data.values()):
        print("Error: No data found for any proc", file=sys.stderr)
        sys.exit(1)

    # Optionally collect setup/init breakdown per proc
    breakdown = None
    if args.show_breakdown:
        proc_dirs = {
            'etcd-shim':               (args.dir_path_etcd,        args.dir_path_etcd_cosandbox),
            'memcached-shim':          (args.dir_path_memcached,   args.dir_path_memcached_cosandbox),
            'cossim-srv-cpp':          (args.dir_path_vecdb,       args.dir_path_vecdb_cosandbox),
            'cached-srv-cpp':          (args.dir_path_cached,      args.dir_path_cached_cosandbox),
            'imgrec_precompiled.wasm': (args.dir_path_imgrec_wasm, args.dir_path_imgrec_wasm_cosandbox),
            'imgrec.py':               (args.dir_path_imgrec_py,   args.dir_path_imgrec_py_cosandbox),
        }
        breakdown = {}
        for proc, (d_without, d_with) in proc_dirs.items():
            breakdown[proc] = {
                'without_cosandbox': get_setup_and_init_times(d_without, proc),  # (setup_span, init_start, init_span)
                'with_cosandbox':    get_setup_and_init_times(d_with, proc),
            }

    # Three graphs: shims, cpp services, imgrec
    shims_procs = ['etcd-shim', 'memcached-shim']
    shims_labels = ['Etcd', 'Memcached']
    cpp_procs = ['cossim-srv-cpp', 'cached-srv-cpp']
    cpp_labels = ['VecDB', 'Cached']
    imgrec_procs = ['imgrec_precompiled.wasm', 'imgrec.py']
    imgrec_labels = ['Imgrec-wasm', 'Imgrec-py']

    def group_values(procs):
        without = [data[p]['without_cosandbox'] if data[p]['without_cosandbox'] is not None else 0 for p in procs]
        with_ = [data[p]['with_cosandbox'] if data[p]['with_cosandbox'] is not None else 0 for p in procs]
        return without, with_

    width = 0.35

    def make_figure(procs, labels, output_path, bd=None):
        from matplotlib.patches import Patch
        without_vals, with_vals = group_values(procs)
        x = np.arange(len(labels))
        fig, ax = plt.subplots(figsize=(5.0, 2.4))

        if bd is not None:
            setup_wo      = [bd[p]['without_cosandbox'][0] or 0 for p in procs]
            init_start_wo = [bd[p]['without_cosandbox'][1] or 0 for p in procs]
            init_wo       = [bd[p]['without_cosandbox'][2] or 0 for p in procs]
            setup_wi      = [bd[p]['with_cosandbox'][0] or 0 for p in procs]
            init_start_wi = [bd[p]['with_cosandbox'][1] or 0 for p in procs]
            init_wi       = [bd[p]['with_cosandbox'][2] or 0 for p in procs]
            # without_vals/with_vals come from get_last_init_time (true end-to-end)

            sub_w  = (width - 0.01) / 2   # each sub-bar width
            offset = sub_w / 2 + 0.005    # distance from outer-bar center to sub-bar center

            # Outline bars showing total end-to-end time
            ax.bar(x - width/2, without_vals, width, fill=False, edgecolor='steelblue', linewidth=1.5)
            ax.bar(x + width/2, with_vals,    width, fill=False, edgecolor='coral',     linewidth=1.5)

            # Setup sub-bars (left half, always starting from 0)
            ax.bar(x - width/2 - offset, setup_wo, sub_w, color='steelblue')
            ax.bar(x + width/2 - offset, setup_wi, sub_w, color='coral')

            # Initialization sub-bars (right half, starting at first init op)
            ax.bar(x - width/2 + offset, init_wo, sub_w, bottom=init_start_wo, color='steelblue', hatch='///', edgecolor='white', linewidth=0)
            ax.bar(x + width/2 + offset, init_wi, sub_w, bottom=init_start_wi, color='coral',     hatch='///', edgecolor='white', linewidth=0)
        else:
            ax.bar(x - width/2, without_vals, width, color='steelblue')
            ax.bar(x + width/2, with_vals,    width, color='coral')

        ax.set_ylabel('Start time (ms)', fontsize=12)
        ax.set_xticks(x)
        ax.set_xticklabels(labels)
        ax.grid(axis='y', alpha=0.3, linestyle='--')
        y_max = max(max(without_vals), max(with_vals))
        ax.set_ylim(0, y_max * 1.15)

        for xpos, h in zip(x - width/2, without_vals):
            if h > 0:
                ax.text(xpos, h, f'{h:.0f}ms', ha='center', va='bottom', fontsize=9)
        for xpos, h in zip(x + width/2, with_vals):
            if h > 0:
                ax.text(xpos, h, f'{h:.0f}ms', ha='center', va='bottom', fontsize=9)

        if bd is not None:
            legend_handles = [
                Patch(facecolor='steelblue', label=f'Without {args.sys_name}'),
                Patch(facecolor='coral',     label=f'With {args.sys_name}'),
                Patch(facecolor='lightgrey', edgecolor='grey', label='Setup'),
                Patch(facecolor='lightgrey', edgecolor='grey', hatch='///', label='Initialization'),
            ]
            ax.legend(handles=legend_handles, loc='lower center', bbox_to_anchor=(0.5, 1.0),
                      ncol=2, fontsize=10, borderpad=0.3, handletextpad=0.5,
                      columnspacing=1.0, frameon=True)
        else:
            legend_handles = [
                Patch(facecolor='steelblue', label=f'Without {args.sys_name}'),
                Patch(facecolor='coral',     label=f'With {args.sys_name}'),
            ]
            ax.legend(handles=legend_handles, loc='lower center', bbox_to_anchor=(0.5, 1.0),
                      ncol=2, fontsize=10, borderpad=0.3, handletextpad=0.5,
                      columnspacing=1.0, frameon=True)

        plt.tight_layout()
        plt.savefig(output_path, dpi=300, bbox_inches='tight')
        print(f"Graph saved to {output_path}")
        plt.close(fig)

    make_figure(shims_procs, shims_labels, args.output_shims, breakdown)
    make_figure(cpp_procs, cpp_labels, args.output_cpp, breakdown)
    make_figure(imgrec_procs, imgrec_labels, args.output_imgrec, breakdown)

    # Print summary statistics
    print("\nSummary:")
    print("=" * 80)
    all_procs = shims_procs + cpp_procs + imgrec_procs
    all_labels = shims_labels + cpp_labels + imgrec_labels
    for i, proc in enumerate(all_procs):
        without = data[proc]['without_cosandbox']
        with_init = data[proc]['with_cosandbox']
        print(f"{all_labels[i]:20} | Without: {without:.2f}ms | With: {with_init:.2f}ms | Diff: {(without - with_init):.2f}ms ({((without - with_init) / without * 100):.1f}%)" if without and with_init else f"{all_labels[i]:20} | Data missing")


if __name__ == "__main__":
    main()

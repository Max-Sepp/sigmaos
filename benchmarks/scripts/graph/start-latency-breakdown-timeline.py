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


def get_detailed_times(dir_path, proc_name):
    """
    Extract the detailed setup and initialization times for a given proc.
    Returns tuple of (setup_events, init_events) where each is a list of
    (op_name, start_time, duration) tuples.
    Start time is calculated as sinceSpawn - op_time.
    If the calculated start time is negative or zero (operation started before/at spawn),
    we position it starting at time 0 instead.
    """
    proc_pid = find_proc_pid(dir_path, proc_name, start=True)
    if proc_pid is None:
        return None, None

    setup_timings, init_timings = find_setup_init_lines(dir_path, proc_pid)

    # Extract events with start times
    setup_events = []
    if setup_timings:
        for op_name, (since_spawn_ms, op_time_ms) in setup_timings.items():
            if op_time_ms is not None:
                if since_spawn_ms is not None:
                    start_time = since_spawn_ms - op_time_ms
                    # If start time is <= 0, the operation started before/at spawn
                    # Position it starting at 0 instead
                    if start_time <= 0:
                        start_time = 0
                else:
                    # If sinceSpawn is not available, start at time 0
                    start_time = 0
                setup_events.append((op_name, start_time, op_time_ms))

    init_events = []
    if init_timings:
        for op_name, (since_spawn_ms, op_time_ms) in init_timings.items():
            if op_time_ms is not None:
                if since_spawn_ms is not None:
                    start_time = since_spawn_ms - op_time_ms
                    # If start time is <= 0, the operation started before/at spawn
                    # Position it starting at 0 instead
                    if start_time <= 0:
                        start_time = 0
                else:
                    # If sinceSpawn is not available, start at time 0
                    start_time = 0
                init_events.append((op_name, start_time, op_time_ms))

    return setup_events, init_events


def main():
    parser = argparse.ArgumentParser(
        description="Create stacked bar graph comparing setup and initialization times for two procs"
    )
    parser.add_argument(
        "--dir_path_1",
        required=True,
        help="Path to first benchmark output directory"
    )
    parser.add_argument(
        "--proc_name_1",
        required=True,
        help="Proc name for first directory"
    )
    parser.add_argument(
        "--dir_path_2",
        required=True,
        help="Path to second benchmark output directory"
    )
    parser.add_argument(
        "--proc_name_2",
        required=True,
        help="Proc name for second directory"
    )
    parser.add_argument(
        "--label_1",
        default=None,
        help="Label for first proc (defaults to proc_name_1)"
    )
    parser.add_argument(
        "--label_2",
        default=None,
        help="Label for second proc (defaults to proc_name_2)"
    )
    parser.add_argument(
        "--output",
        default="start-latency-breakdown-timeline.png",
        help="Output filename for the graph (default: start-latency-breakdown-timeline.png)"
    )

    args = parser.parse_args()

    # Use proc names as labels if not specified
    label_1 = args.label_1 if args.label_1 else args.proc_name_1
    label_2 = args.label_2 if args.label_2 else args.proc_name_2

    # Extract data for both procs
    setup_events_1, init_events_1 = get_detailed_times(args.dir_path_1, args.proc_name_1)
    setup_events_2, init_events_2 = get_detailed_times(args.dir_path_2, args.proc_name_2)

    if setup_events_1 is None or init_events_1 is None:
        print(f"Error: Could not extract data for {args.proc_name_1} from {args.dir_path_1}", file=sys.stderr)
        sys.exit(1)

    if setup_events_2 is None or init_events_2 is None:
        print(f"Error: Could not extract data for {args.proc_name_2} from {args.dir_path_2}", file=sys.stderr)
        sys.exit(1)

    # Combine all events for each proc
    all_events_1 = setup_events_1 + init_events_1
    all_events_2 = setup_events_2 + init_events_2

    # Collect all unique operation names
    all_ops = set()
    for op_name, _, _ in all_events_1 + all_events_2:
        all_ops.add(op_name)
    all_ops = sorted(all_ops)

    # Generate a color palette for all operations
    import matplotlib.cm as cm
    colors = cm.get_cmap('tab20')(np.linspace(0, 1, len(all_ops)))
    op_colors = {op: colors[i] for i, op in enumerate(all_ops)}

    def assign_lanes(events):
        """
        Assign vertical lanes to events in a stair-step pattern.
        Each event gets its own lane based on its order (sorted by start time).
        Returns a list of (op_name, start_time, duration, lane) tuples.
        """
        # Sort events by start time
        sorted_events = sorted(events, key=lambda x: x[1])

        event_lanes = []
        for lane, (op_name, start_time, duration) in enumerate(sorted_events):
            event_lanes.append((op_name, start_time, duration, lane))

        return event_lanes

    # Assign lanes to avoid overlaps
    events_with_lanes_1 = assign_lanes(all_events_1)
    events_with_lanes_2 = assign_lanes(all_events_2)

    # Calculate how many lanes we need for each proc
    max_lanes_1 = max([lane for _, _, _, lane in events_with_lanes_1]) + 1 if events_with_lanes_1 else 1
    max_lanes_2 = max([lane for _, _, _, lane in events_with_lanes_2]) + 1 if events_with_lanes_2 else 1

    # Create timeline plot with enough vertical space
    lane_height = 0.3
    spacing_between_procs = 1.0
    total_height_1 = max_lanes_1 * lane_height
    total_height_2 = max_lanes_2 * lane_height

    fig_height = max(4, (total_height_1 + total_height_2 + spacing_between_procs) * 0.8)
    fig, ax = plt.subplots(figsize=(10, fig_height))

    # Calculate base y-positions for each proc's timeline
    base_y_1 = total_height_2 + spacing_between_procs
    base_y_2 = 0

    # Plot events for proc 1
    for op_name, start_time, duration, lane in events_with_lanes_1:
        y_pos = base_y_1 + lane * lane_height
        # Ensure minimum visible width for very small durations
        visible_duration = max(duration, 1.0)
        ax.barh(y_pos, visible_duration, left=start_time, height=lane_height * 0.9,
               color=op_colors[op_name], edgecolor='black', linewidth=0.5)

        # Add label to the right of the bar
        ax.text(start_time + visible_duration, y_pos,
               f'{op_name} ({duration:.0f}ms)',
               ha='left', va='center', fontsize=7,
               color='black')

    # Plot events for proc 2
    for op_name, start_time, duration, lane in events_with_lanes_2:
        y_pos = base_y_2 + lane * lane_height
        # Ensure minimum visible width for very small durations
        visible_duration = max(duration, 1.0)
        ax.barh(y_pos, visible_duration, left=start_time, height=lane_height * 0.9,
               color=op_colors[op_name], edgecolor='black', linewidth=0.5)

        # Add label to the right of the bar
        ax.text(start_time + visible_duration, y_pos,
               f'{op_name} ({duration:.0f}ms)',
               ha='left', va='center', fontsize=7,
               color='black')

    # Customize the plot
    ax.set_xlabel('Time since spawn (ms)', fontsize=12)

    # Set y-ticks at the center of each proc's timeline
    y_tick_1 = base_y_1 + (max_lanes_1 * lane_height) / 2
    y_tick_2 = base_y_2 + (max_lanes_2 * lane_height) / 2
    ax.set_yticks([y_tick_2, y_tick_1])
    ax.set_yticklabels([label_2, label_1], rotation=90, va='center')

    ax.set_ylim(-lane_height, base_y_1 + total_height_1 + lane_height)

    # Find max time across both timelines
    max_time_1 = max([start + dur for _, start, dur in all_events_1]) if all_events_1 else 0
    max_time_2 = max([start + dur for _, start, dur in all_events_2]) if all_events_2 else 0
    max_time = max(max_time_1, max_time_2)
    ax.set_xlim(0, max_time * 1.05)

    # Create legend
    from matplotlib.patches import Patch
    legend_elements = [Patch(facecolor=op_colors[op], label=op) for op in all_ops]
    ax.legend(handles=legend_elements, loc='upper left', bbox_to_anchor=(1, 1), fontsize=8)

    ax.grid(axis='x', alpha=0.3, linestyle='--')

    plt.tight_layout()
    plt.savefig(args.output, dpi=300, bbox_inches='tight')
    print(f"Graph saved to {args.output}")

    # Print summary statistics
    print("\nSummary:")
    print("=" * 80)

    # Calculate totals for each proc
    total_1 = sum([dur for _, _, dur in all_events_1])
    setup_total_1 = sum([dur for _, _, dur in setup_events_1])
    init_total_1 = sum([dur for _, _, dur in init_events_1])

    print(f"{label_1}:")
    print(f"  Setup operations:")
    for op_name, start_time, duration in sorted(setup_events_1, key=lambda x: x[1]):
        print(f"    {op_name:<40} {start_time:.2f}ms -> {start_time + duration:.2f}ms ({duration:.2f}ms)")
    print(f"  Setup Total:    {setup_total_1:.2f}ms")
    print()
    print(f"  Initialization operations:")
    for op_name, start_time, duration in sorted(init_events_1, key=lambda x: x[1]):
        print(f"    {op_name:<40} {start_time:.2f}ms -> {start_time + duration:.2f}ms ({duration:.2f}ms)")
    print(f"  Init Total:     {init_total_1:.2f}ms")
    print(f"  TOTAL:          {total_1:.2f}ms")
    print()

    total_2 = sum([dur for _, _, dur in all_events_2])
    setup_total_2 = sum([dur for _, _, dur in setup_events_2])
    init_total_2 = sum([dur for _, _, dur in init_events_2])

    print(f"{label_2}:")
    print(f"  Setup operations:")
    for op_name, start_time, duration in sorted(setup_events_2, key=lambda x: x[1]):
        print(f"    {op_name:<40} {start_time:.2f}ms -> {start_time + duration:.2f}ms ({duration:.2f}ms)")
    print(f"  Setup Total:    {setup_total_2:.2f}ms")
    print()
    print(f"  Initialization operations:")
    for op_name, start_time, duration in sorted(init_events_2, key=lambda x: x[1]):
        print(f"    {op_name:<40} {start_time:.2f}ms -> {start_time + duration:.2f}ms ({duration:.2f}ms)")
    print(f"  Init Total:     {init_total_2:.2f}ms")
    print(f"  TOTAL:          {total_2:.2f}ms")
    print()

    # Comparison
    if total_1 > 0 and total_2 > 0:
        diff = total_1 - total_2
        pct = (diff / total_1) * 100
        print(f"Difference (total): {diff:.2f}ms ({pct:.1f}%)")


if __name__ == "__main__":
    main()

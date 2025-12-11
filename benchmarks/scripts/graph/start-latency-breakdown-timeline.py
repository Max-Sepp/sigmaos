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


def parse_timing_line(line, paper_mode=False):
    """
    Parse a log line to extract phase, operation name, sinceSpawn, and op timing.
    Expected format: [proc_pid] Setup.OperationName or Initialization.OperationName ... sinceSpawn:123ms ... op:456ms
    If paper_mode is True, only matches Paper.Setup.* and Paper.Initialization.*
    Returns tuple of (phase, op_name, since_spawn_ms, op_time_ms) or None if parsing fails
    """
    # Extract the phase (Setup or Initialization) and operation name
    if paper_mode:
        op_match = re.search(r'\] Paper\.(Setup|Initialization)\.(\S+)', line)
    else:
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


def find_setup_init_lines(dir_path, proc_pid, paper_mode=False):
    """
    Search all log files in dir_path/sigmaos-node-logs for lines matching
    "[proc_pid] Setup\\.*" or "[proc_pid] Initialization\\.*"
    If paper_mode is True, matches "[proc_pid] Paper.Setup\\.*" or "[proc_pid] Paper.Initialization\\.*"
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
    if paper_mode:
        setup_pattern = re.compile(rf"\[{re.escape(proc_pid)}\] Paper\.Setup\..*")
        init_pattern = re.compile(rf"\[{re.escape(proc_pid)}\] Paper\.Initialization\..*")
    else:
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
                        parsed = parse_timing_line(line, paper_mode)
                        if parsed:
                            phase, op_name, since_spawn_ms, op_time_ms = parsed
                            if phase == "Setup":
                                setup_timings[op_name] = (since_spawn_ms, op_time_ms)
                            else:  # Initialization
                                init_timings[op_name] = (since_spawn_ms, op_time_ms)
        except Exception as e:
            print(f"Warning: Could not read {log_file}: {e}", file=sys.stderr)

    return setup_timings, init_timings


def subtract_event_durations(events_target, events_source, subtract_pairs):
    """
    Subtract durations from source events from target events based on label pairs.
    For each pair (source_label, target_label), find the event with source_label in events_source,
    and subtract its duration from the event with target_label in events_target.

    Args:
        events_target: List of (op_name, start_time, duration) tuples to modify
        events_source: List of (op_name, start_time, duration) tuples to subtract from
        subtract_pairs: List of (source_label, target_label) pairs

    Returns:
        New list of events with durations adjusted
    """
    if not subtract_pairs:
        return events_target

    # Build a dict from events_source for quick lookup
    source_dict = {}
    for op_name, start_time, duration in events_source:
        if op_name not in source_dict:
            source_dict[op_name] = duration
        else:
            # If multiple events with same name, sum them
            source_dict[op_name] += duration

    # Apply subtractions to target events
    result = []
    for op_name, start_time, duration in events_target:
        new_duration = duration

        # Check if this event should have something subtracted from it
        for source_label, target_label in subtract_pairs:
            if op_name == target_label and source_label in source_dict:
                new_duration -= source_dict[source_label]
                # Ensure duration doesn't go negative
                if new_duration < 0:
                    print(f"Warning: Subtraction resulted in negative duration for {op_name}. Setting to 0.", file=sys.stderr)
                    new_duration = 0

        result.append((op_name, start_time, new_duration))

    return result


def relabel_events(events, relabel_pairs):
    """
    Rename event labels based on relabel pairs.
    For each pair (old_label, new_label), rename all events with old_label to new_label.

    Args:
        events: List of (op_name, start_time, duration) tuples
        relabel_pairs: List of (old_label, new_label) pairs

    Returns:
        New list of events with specified labels renamed
    """
    if not relabel_pairs:
        return events

    # Create a mapping from old labels to new labels
    relabel_map = {old: new for old, new in relabel_pairs}

    # Apply relabeling
    result = []
    for op_name, start_time, duration in events:
        new_name = relabel_map.get(op_name, op_name)
        result.append((new_name, start_time, duration))

    return result


def combine_events(events, combine_pairs):
    """
    Combine events based on label pairs.
    For each pair (label1, label2), find events with label1 and label2,
    combine them into a single event with label2 and the combined duration.
    The combined event starts at the earlier start time.

    Args:
        events: List of (op_name, start_time, duration) tuples
        combine_pairs: List of (label1, label2) pairs

    Returns:
        New list of events with specified labels combined
    """
    # Convert events to a dict for easier lookup
    events_dict = {}
    for op_name, start_time, duration in events:
        if op_name not in events_dict:
            events_dict[op_name] = []
        events_dict[op_name].append((start_time, duration))

    # Process each combine pair
    for label1, label2 in combine_pairs:
        if label1 in events_dict and label2 in events_dict:
            # Get all events for both labels
            events1 = events_dict[label1]
            events2 = events_dict[label2]

            # For each combination, merge them
            # Take the earliest start time
            all_starts = [s for s, d in events1 + events2]
            all_ends = [s + d for s, d in events1 + events2]
            combined_start = min(all_starts)
            combined_end = max(all_ends)
            combined_duration = combined_end - combined_start

            # Remove label1 and update label2
            del events_dict[label1]
            events_dict[label2] = [(combined_start, combined_duration)]

    # Convert back to list of tuples
    result = []
    for op_name, event_list in events_dict.items():
        for start_time, duration in event_list:
            result.append((op_name, start_time, duration))

    return result


def get_detailed_times(dir_path, proc_name, paper_mode=False):
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

    setup_timings, init_timings = find_setup_init_lines(dir_path, proc_pid, paper_mode)

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
    parser.add_argument(
        "--paper",
        action="store_true",
        help="Only match Paper.Setup.* and Paper.Initialization.* log lines"
    )
    parser.add_argument(
        "--combine_1",
        action="append",
        nargs=2,
        metavar=("LABEL1", "LABEL2"),
        help="Combine two labels in proc 1 into a single bar named LABEL2. Can be specified multiple times. Format: --combine_1 LABEL1 LABEL2"
    )
    parser.add_argument(
        "--combine_2",
        action="append",
        nargs=2,
        metavar=("LABEL1", "LABEL2"),
        help="Combine two labels in proc 2 into a single bar named LABEL2. Can be specified multiple times. Format: --combine_2 LABEL1 LABEL2"
    )
    parser.add_argument(
        "--omit_1",
        action="append",
        metavar="LABEL",
        help="Omit bars with this label from proc 1. Can be specified multiple times."
    )
    parser.add_argument(
        "--omit_2",
        action="append",
        metavar="LABEL",
        help="Omit bars with this label from proc 2. Can be specified multiple times."
    )
    parser.add_argument(
        "--relabel_1",
        action="append",
        nargs=2,
        metavar=("OLD_LABEL", "NEW_LABEL"),
        help="Rename a label in proc 1 from OLD_LABEL to NEW_LABEL. Can be specified multiple times. Format: --relabel_1 OLD_LABEL NEW_LABEL"
    )
    parser.add_argument(
        "--relabel_2",
        action="append",
        nargs=2,
        metavar=("OLD_LABEL", "NEW_LABEL"),
        help="Rename a label in proc 2 from OLD_LABEL to NEW_LABEL. Can be specified multiple times. Format: --relabel_2 OLD_LABEL NEW_LABEL"
    )
    parser.add_argument(
        "--subtract_1_from_2",
        action="append",
        nargs=2,
        metavar=("PROC1_LABEL", "PROC2_LABEL"),
        help="Subtract the duration of PROC1_LABEL (from proc 1) from PROC2_LABEL (from proc 2). The events can have different names. Can be specified multiple times. Format: --subtract_1_from_2 PROC1_LABEL PROC2_LABEL"
    )
    parser.add_argument(
        "--subtract_2_from_1",
        action="append",
        nargs=2,
        metavar=("PROC2_LABEL", "PROC1_LABEL"),
        help="Subtract the duration of PROC2_LABEL (from proc 2) from PROC1_LABEL (from proc 1). The events can have different names. Can be specified multiple times. Format: --subtract_2_from_1 PROC2_LABEL PROC1_LABEL"
    )
    parser.add_argument(
        "--simplified",
        action="store_true",
        help="Combine all setup events into one bar and all initialization events into another bar for each proc"
    )

    args = parser.parse_args()

    # Use proc names as labels if not specified
    label_1 = args.label_1 if args.label_1 else args.proc_name_1
    label_2 = args.label_2 if args.label_2 else args.proc_name_2

    # Extract data for both procs
    setup_events_1, init_events_1 = get_detailed_times(args.dir_path_1, args.proc_name_1, args.paper)
    setup_events_2, init_events_2 = get_detailed_times(args.dir_path_2, args.proc_name_2, args.paper)

    if setup_events_1 is None or init_events_1 is None:
        print(f"Error: Could not extract data for {args.proc_name_1} from {args.dir_path_1}", file=sys.stderr)
        sys.exit(1)

    if setup_events_2 is None or init_events_2 is None:
        print(f"Error: Could not extract data for {args.proc_name_2} from {args.dir_path_2}", file=sys.stderr)
        sys.exit(1)

    # Combine all events for each proc
    all_events_1 = setup_events_1 + init_events_1
    all_events_2 = setup_events_2 + init_events_2

    # Apply relabeling first (before tracking phase names)
    if args.relabel_1:
        all_events_1 = relabel_events(all_events_1, args.relabel_1)
        setup_events_1 = relabel_events(setup_events_1, args.relabel_1)
        init_events_1 = relabel_events(init_events_1, args.relabel_1)

    if args.relabel_2:
        all_events_2 = relabel_events(all_events_2, args.relabel_2)
        setup_events_2 = relabel_events(setup_events_2, args.relabel_2)
        init_events_2 = relabel_events(init_events_2, args.relabel_2)

    # Track which operation names were originally in setup vs init (after relabeling)
    setup_op_names_1 = set(e[0] for e in setup_events_1)
    init_op_names_1 = set(e[0] for e in init_events_1)
    setup_op_names_2 = set(e[0] for e in setup_events_2)
    init_op_names_2 = set(e[0] for e in init_events_2)

    # Build a mapping for combined labels
    # If label1 is combined into label2, label2 should inherit label1's phase
    def build_phase_mapping(combine_pairs, setup_names, init_names):
        """Build mapping from combined label to phase (setup or init)"""
        phase_map = {}
        for label1, label2 in combine_pairs or []:
            # If label1 was in setup, label2 should be considered setup
            # If label1 was in init, label2 should be considered init
            if label1 in setup_names:
                phase_map[label2] = 'setup'
            elif label1 in init_names:
                phase_map[label2] = 'init'
        return phase_map

    phase_map_1 = build_phase_mapping(args.combine_1, setup_op_names_1, init_op_names_1)
    phase_map_2 = build_phase_mapping(args.combine_2, setup_op_names_2, init_op_names_2)

    # Update the phase sets with the combined mappings
    for label, phase in phase_map_1.items():
        if phase == 'setup':
            setup_op_names_1.add(label)
        else:
            init_op_names_1.add(label)

    for label, phase in phase_map_2.items():
        if phase == 'setup':
            setup_op_names_2.add(label)
        else:
            init_op_names_2.add(label)

    # Apply label combinations for proc 1
    if args.combine_1:
        all_events_1 = combine_events(all_events_1, args.combine_1)

    # Apply label combinations for proc 2
    if args.combine_2:
        all_events_2 = combine_events(all_events_2, args.combine_2)

    # Apply subtraction operations (after relabeling and combining, but before omitting)
    if args.subtract_1_from_2:
        all_events_2 = subtract_event_durations(all_events_2, all_events_1, args.subtract_1_from_2)

    if args.subtract_2_from_1:
        all_events_1 = subtract_event_durations(all_events_1, all_events_2, args.subtract_2_from_1)

    # Apply omit filters for proc 1
    if args.omit_1:
        all_events_1 = [e for e in all_events_1 if e[0] not in args.omit_1]

    # Apply omit filters for proc 2
    if args.omit_2:
        all_events_2 = [e for e in all_events_2 if e[0] not in args.omit_2]

    # Re-split into setup and init events for proc 1
    setup_events_1 = [e for e in all_events_1 if e[0] in setup_op_names_1]
    init_events_1 = [e for e in all_events_1 if e[0] in init_op_names_1]

    # Re-split into setup and init events for proc 2
    setup_events_2 = [e for e in all_events_2 if e[0] in setup_op_names_2]
    init_events_2 = [e for e in all_events_2 if e[0] in init_op_names_2]

    # Apply simplified mode if requested
    if args.simplified:
        # Combine all setup events into one bar for proc 1
        if setup_events_1:
            all_starts_setup_1 = [s for _, s, d in setup_events_1]
            all_ends_setup_1 = [s + d for _, s, d in setup_events_1]
            combined_start_setup_1 = min(all_starts_setup_1)
            combined_end_setup_1 = max(all_ends_setup_1)
            combined_duration_setup_1 = combined_end_setup_1 - combined_start_setup_1
            setup_events_1 = [("Setup", combined_start_setup_1, combined_duration_setup_1)]

        # Combine all init events into one bar for proc 1
        if init_events_1:
            all_starts_init_1 = [s for _, s, d in init_events_1]
            all_ends_init_1 = [s + d for _, s, d in init_events_1]
            combined_start_init_1 = min(all_starts_init_1)
            combined_end_init_1 = max(all_ends_init_1)
            combined_duration_init_1 = combined_end_init_1 - combined_start_init_1
            init_events_1 = [("Initialization", combined_start_init_1, combined_duration_init_1)]

        # Combine all setup events into one bar for proc 2
        if setup_events_2:
            all_starts_setup_2 = [s for _, s, d in setup_events_2]
            all_ends_setup_2 = [s + d for _, s, d in setup_events_2]
            combined_start_setup_2 = min(all_starts_setup_2)
            combined_end_setup_2 = max(all_ends_setup_2)
            combined_duration_setup_2 = combined_end_setup_2 - combined_start_setup_2
            setup_events_2 = [("Setup", combined_start_setup_2, combined_duration_setup_2)]

        # Combine all init events into one bar for proc 2
        if init_events_2:
            all_starts_init_2 = [s for _, s, d in init_events_2]
            all_ends_init_2 = [s + d for _, s, d in init_events_2]
            combined_start_init_2 = min(all_starts_init_2)
            combined_end_init_2 = max(all_ends_init_2)
            combined_duration_init_2 = combined_end_init_2 - combined_start_init_2
            init_events_2 = [("Initialization", combined_start_init_2, combined_duration_init_2)]

        # Update all_events to include the simplified events
        all_events_1 = setup_events_1 + init_events_1
        all_events_2 = setup_events_2 + init_events_2

        # Update phase name sets for simplified mode
        setup_op_names_1 = {"Setup"}
        init_op_names_1 = {"Initialization"}
        setup_op_names_2 = {"Setup"}
        init_op_names_2 = {"Initialization"}

    # Define colors for each phase
    phase_colors = {
        'setup': 'steelblue',
        'init': 'coral'
    }

    # Create a mapping from operation name to phase (setup or init)
    def get_phase_color(op_name, setup_names, init_names):
        """Get the color for an operation based on its phase."""
        if op_name in setup_names:
            return phase_colors['setup']
        elif op_name in init_names:
            return phase_colors['init']
        else:
            return 'gray'  # Default color if not in either phase

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
        # Get color based on phase
        bar_color = get_phase_color(op_name, setup_op_names_1, init_op_names_1)
        ax.barh(y_pos, visible_duration, left=start_time, height=lane_height * 0.9,
               color=bar_color, edgecolor='black', linewidth=0.5)

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
        # Get color based on phase
        bar_color = get_phase_color(op_name, setup_op_names_2, init_op_names_2)
        ax.barh(y_pos, visible_duration, left=start_time, height=lane_height * 0.9,
               color=bar_color, edgecolor='black', linewidth=0.5)

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
    ax.set_xlim(0, max_time * 1.15)

    ax.grid(axis='x', alpha=0.3, linestyle='--')

    # Add legend for phases
    from matplotlib.patches import Patch
    legend_elements = [
        Patch(facecolor=phase_colors['setup'], edgecolor='black', label='Setup'),
        Patch(facecolor=phase_colors['init'], edgecolor='black', label='Initialization')
    ]
    ax.legend(handles=legend_elements, loc='lower right', fontsize=10)

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

#!/usr/bin/env python3

import argparse
import glob
import os
import re
import sys
import matplotlib.pyplot as plt
import numpy as np


def parse_completion_time(line):
    """
    Parse a line containing "Total time to completion since exec: " and extract the time.
    Expected format: ... Total time to completion since exec: XXX.XXms
    Returns the time in milliseconds as a float, or None if parsing fails.
    """
    if "Total time to completion since exec: " not in line:
        return None

    # Extract the last word
    last_word = line.strip().split()[-1]

    # Parse the time value and unit
    match = re.match(r'(\d+(?:\.\d+)?)(ms|µs|us|s)', last_word)
    if not match:
        return None

    timing_value = float(match.group(1))
    timing_unit = match.group(2)

    # Convert to milliseconds
    if timing_unit in ['µs', 'us']:
        time_ms = timing_value / 1000.0
    elif timing_unit == 's':
        time_ms = timing_value * 1000.0
    else:  # ms
        time_ms = timing_value

    return time_ms


def extract_completion_times(dir_path):
    """
    Search all log files in dir_path/sigmaos-node-logs for lines containing
    "Total time to completion since exec: " and extract the completion times.
    Returns a list of completion times in milliseconds.
    """
    log_dir = os.path.join(dir_path, "sigmaos-node-logs")

    if not os.path.isdir(log_dir):
        print(f"Error: Directory {log_dir} does not exist", file=sys.stderr)
        return []

    log_files = glob.glob(os.path.join(log_dir, "*"))

    if not log_files:
        print(f"Error: No log files found in {log_dir}", file=sys.stderr)
        return []

    completion_times = []

    for log_file in sorted(log_files):
        # Skip directories
        if os.path.isdir(log_file):
            continue

        try:
            with open(log_file, 'r') as f:
                for line in f:
                    time_ms = parse_completion_time(line)
                    if time_ms is not None:
                        completion_times.append(time_ms)
        except Exception as e:
            print(f"Warning: Could not read {log_file}: {e}", file=sys.stderr)

    return completion_times


def main():
    parser = argparse.ArgumentParser(
        description="Create bar graph comparing image resize completion times"
    )
    parser.add_argument(
        "--dir_path_nocosandboxes",
        required=True,
        help="Path to benchmark output directory without cosandboxes"
    )
    parser.add_argument(
        "--dir_path_cosandboxes",
        required=True,
        help="Path to benchmark output directory with cosandboxes"
    )
    parser.add_argument(
        "--dir_path_cosandboxes_writeout",
        required=False,
        help="Path to benchmark output directory with cosandboxes and writeout (optional)"
    )
    parser.add_argument(
        "--output",
        default="imgresize-time-comparison.png",
        help="Output filename for the graph (default: imgresize-time-comparison.png)"
    )

    args = parser.parse_args()

    # Extract completion times for both configurations
    times_nocosandboxes = extract_completion_times(args.dir_path_nocosandboxes)
    times_cosandboxes = extract_completion_times(args.dir_path_cosandboxes)

    # Extract completion times for third configuration if provided
    times_cosandboxes_writeout = []
    if args.dir_path_cosandboxes_writeout:
        times_cosandboxes_writeout = extract_completion_times(args.dir_path_cosandboxes_writeout)

    if not times_nocosandboxes:
        print(f"Warning: No completion times found in {args.dir_path_nocosandboxes}", file=sys.stderr)
    if not times_cosandboxes:
        print(f"Warning: No completion times found in {args.dir_path_cosandboxes}", file=sys.stderr)
    if args.dir_path_cosandboxes_writeout and not times_cosandboxes_writeout:
        print(f"Warning: No completion times found in {args.dir_path_cosandboxes_writeout}", file=sys.stderr)

    if not times_nocosandboxes and not times_cosandboxes and not times_cosandboxes_writeout:
        print("Error: No data found in any directory", file=sys.stderr)
        sys.exit(1)

    # Calculate averages
    avg_nocosandboxes = np.mean(times_nocosandboxes) if times_nocosandboxes else 0
    avg_cosandboxes = np.mean(times_cosandboxes) if times_cosandboxes else 0
    avg_cosandboxes_writeout = np.mean(times_cosandboxes_writeout) if times_cosandboxes_writeout else 0

    # Prepare data for plotting
    if args.dir_path_cosandboxes_writeout:
        labels = ['No co-sandbox', 'co-sandbox (input)', 'co-sandbox (input+output)']
        averages = [avg_nocosandboxes, avg_cosandboxes, avg_cosandboxes_writeout]
        colors = ['steelblue', 'coral', 'mediumseagreen']
    else:
        labels = ['Without co-sandbox', 'With co-sandbox']
        averages = [avg_nocosandboxes, avg_cosandboxes]
        colors = ['steelblue', 'coral']

    # Create bar graph
    fig, ax = plt.subplots(figsize=(6.4, 2.4))
    bars = ax.bar(labels, averages, color=colors)

    # Customize the plot
    ax.set_ylabel('Time (ms)', fontsize=12)
    ax.grid(axis='y', alpha=0.3, linestyle='--')

    # Add value labels on top of bars
    for bar in bars:
        height = bar.get_height()
        if height > 0:
            ax.text(bar.get_x() + bar.get_width()/2., height,
                   f'{height:.0f}ms',
                   ha='center', va='bottom', fontsize=10)

    # Add headroom at the top for labels
    y_max = max(averages)
    if y_max > 0:
        ax.set_ylim(0, y_max * 1.15)

    plt.tight_layout()
    plt.savefig(args.output, dpi=300, bbox_inches='tight')
    print(f"Graph saved to {args.output}")

    # Print summary statistics
    print("\nSummary:")
    print("=" * 80)

    # Without CoSandboxes stats
    if times_nocosandboxes:
        std_nocosandboxes = np.std(times_nocosandboxes)
        median_nocosandboxes = np.median(times_nocosandboxes)
        p90_nocosandboxes = np.percentile(times_nocosandboxes, 90)
        max_nocosandboxes = np.max(times_nocosandboxes)
        print(f"Without CoSandboxes: {len(times_nocosandboxes)} samples")
        print(f"  Avg:    {avg_nocosandboxes:.2f}ms")
        print(f"  Median: {median_nocosandboxes:.2f}ms")
        print(f"  StdDev: {std_nocosandboxes:.2f}ms")
        print(f"  90th percentile: {p90_nocosandboxes:.2f}ms")
        print(f"  Max:    {max_nocosandboxes:.2f}ms")
    else:
        print("Without CoSandboxes: No data")

    print()

    # With CoSandboxes stats
    if times_cosandboxes:
        std_cosandboxes = np.std(times_cosandboxes)
        median_cosandboxes = np.median(times_cosandboxes)
        p90_cosandboxes = np.percentile(times_cosandboxes, 90)
        max_cosandboxes = np.max(times_cosandboxes)
        print(f"With CoSandboxes:    {len(times_cosandboxes)} samples")
        print(f"  Avg:    {avg_cosandboxes:.2f}ms")
        print(f"  Median: {median_cosandboxes:.2f}ms")
        print(f"  StdDev: {std_cosandboxes:.2f}ms")
        print(f"  90th percentile: {p90_cosandboxes:.2f}ms")
        print(f"  Max:    {max_cosandboxes:.2f}ms")
    else:
        print("With CoSandboxes: No data")

    print()

    # With CoSandboxes + Writeout stats (if provided)
    if times_cosandboxes_writeout:
        std_cosandboxes_writeout = np.std(times_cosandboxes_writeout)
        median_cosandboxes_writeout = np.median(times_cosandboxes_writeout)
        p90_cosandboxes_writeout = np.percentile(times_cosandboxes_writeout, 90)
        max_cosandboxes_writeout = np.max(times_cosandboxes_writeout)
        print(f"With CoSandboxes + Writeout: {len(times_cosandboxes_writeout)} samples")
        print(f"  Avg:    {avg_cosandboxes_writeout:.2f}ms")
        print(f"  Median: {median_cosandboxes_writeout:.2f}ms")
        print(f"  StdDev: {std_cosandboxes_writeout:.2f}ms")
        print(f"  90th percentile: {p90_cosandboxes_writeout:.2f}ms")
        print(f"  Max:    {max_cosandboxes_writeout:.2f}ms")
        print()

    # Comparison
    if avg_nocosandboxes > 0 and avg_cosandboxes > 0:
        diff = avg_nocosandboxes - avg_cosandboxes
        pct = (diff / avg_nocosandboxes) * 100
        print(f"Difference (avg no-cosandboxes vs cosandboxes): {diff:.2f}ms ({pct:.1f}%)")

    if avg_nocosandboxes > 0 and avg_cosandboxes_writeout > 0:
        diff = avg_nocosandboxes - avg_cosandboxes_writeout
        pct = (diff / avg_nocosandboxes) * 100
        print(f"Difference (avg no-cosandboxes vs cosandboxes+writeout): {diff:.2f}ms ({pct:.1f}%)")


if __name__ == "__main__":
    main()

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
        "--dir_path_noinitscripts",
        required=True,
        help="Path to benchmark output directory without initscripts"
    )
    parser.add_argument(
        "--dir_path_initscripts",
        required=True,
        help="Path to benchmark output directory with initscripts"
    )
    parser.add_argument(
        "--dir_path_initscripts_writeout",
        required=False,
        help="Path to benchmark output directory with initscripts and writeout (optional)"
    )
    parser.add_argument(
        "--output",
        default="imgresize-time-comparison.png",
        help="Output filename for the graph (default: imgresize-time-comparison.png)"
    )

    args = parser.parse_args()

    # Extract completion times for both configurations
    times_noinitscripts = extract_completion_times(args.dir_path_noinitscripts)
    times_initscripts = extract_completion_times(args.dir_path_initscripts)

    # Extract completion times for third configuration if provided
    times_initscripts_writeout = []
    if args.dir_path_initscripts_writeout:
        times_initscripts_writeout = extract_completion_times(args.dir_path_initscripts_writeout)

    if not times_noinitscripts:
        print(f"Warning: No completion times found in {args.dir_path_noinitscripts}", file=sys.stderr)
    if not times_initscripts:
        print(f"Warning: No completion times found in {args.dir_path_initscripts}", file=sys.stderr)
    if args.dir_path_initscripts_writeout and not times_initscripts_writeout:
        print(f"Warning: No completion times found in {args.dir_path_initscripts_writeout}", file=sys.stderr)

    if not times_noinitscripts and not times_initscripts and not times_initscripts_writeout:
        print("Error: No data found in any directory", file=sys.stderr)
        sys.exit(1)

    # Calculate averages
    avg_noinitscripts = np.mean(times_noinitscripts) if times_noinitscripts else 0
    avg_initscripts = np.mean(times_initscripts) if times_initscripts else 0
    avg_initscripts_writeout = np.mean(times_initscripts_writeout) if times_initscripts_writeout else 0

    # Prepare data for plotting
    if args.dir_path_initscripts_writeout:
        labels = ['Without co-sandbox', 'With co-sandbox', 'With co-sandbox + writeout']
        averages = [avg_noinitscripts, avg_initscripts, avg_initscripts_writeout]
        colors = ['steelblue', 'coral', 'mediumseagreen']
    else:
        labels = ['Without co-sandbox', 'With co-sandbox']
        averages = [avg_noinitscripts, avg_initscripts]
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

    # Without Initscripts stats
    if times_noinitscripts:
        std_noinitscripts = np.std(times_noinitscripts)
        median_noinitscripts = np.median(times_noinitscripts)
        p90_noinitscripts = np.percentile(times_noinitscripts, 90)
        max_noinitscripts = np.max(times_noinitscripts)
        print(f"Without Initscripts: {len(times_noinitscripts)} samples")
        print(f"  Avg:    {avg_noinitscripts:.2f}ms")
        print(f"  Median: {median_noinitscripts:.2f}ms")
        print(f"  StdDev: {std_noinitscripts:.2f}ms")
        print(f"  90th percentile: {p90_noinitscripts:.2f}ms")
        print(f"  Max:    {max_noinitscripts:.2f}ms")
    else:
        print("Without Initscripts: No data")

    print()

    # With Initscripts stats
    if times_initscripts:
        std_initscripts = np.std(times_initscripts)
        median_initscripts = np.median(times_initscripts)
        p90_initscripts = np.percentile(times_initscripts, 90)
        max_initscripts = np.max(times_initscripts)
        print(f"With Initscripts:    {len(times_initscripts)} samples")
        print(f"  Avg:    {avg_initscripts:.2f}ms")
        print(f"  Median: {median_initscripts:.2f}ms")
        print(f"  StdDev: {std_initscripts:.2f}ms")
        print(f"  90th percentile: {p90_initscripts:.2f}ms")
        print(f"  Max:    {max_initscripts:.2f}ms")
    else:
        print("With Initscripts: No data")

    print()

    # With Initscripts + Writeout stats (if provided)
    if times_initscripts_writeout:
        std_initscripts_writeout = np.std(times_initscripts_writeout)
        median_initscripts_writeout = np.median(times_initscripts_writeout)
        p90_initscripts_writeout = np.percentile(times_initscripts_writeout, 90)
        max_initscripts_writeout = np.max(times_initscripts_writeout)
        print(f"With Initscripts + Writeout: {len(times_initscripts_writeout)} samples")
        print(f"  Avg:    {avg_initscripts_writeout:.2f}ms")
        print(f"  Median: {median_initscripts_writeout:.2f}ms")
        print(f"  StdDev: {std_initscripts_writeout:.2f}ms")
        print(f"  90th percentile: {p90_initscripts_writeout:.2f}ms")
        print(f"  Max:    {max_initscripts_writeout:.2f}ms")
        print()

    # Comparison
    if avg_noinitscripts > 0 and avg_initscripts > 0:
        diff = avg_noinitscripts - avg_initscripts
        pct = (diff / avg_noinitscripts) * 100
        print(f"Difference (avg no-initscripts vs initscripts): {diff:.2f}ms ({pct:.1f}%)")

    if avg_noinitscripts > 0 and avg_initscripts_writeout > 0:
        diff = avg_noinitscripts - avg_initscripts_writeout
        pct = (diff / avg_noinitscripts) * 100
        print(f"Difference (avg no-initscripts vs initscripts+writeout): {diff:.2f}ms ({pct:.1f}%)")


if __name__ == "__main__":
    main()

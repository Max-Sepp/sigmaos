#!/usr/bin/env python3

import argparse
import glob
import os
import re
import sys
import matplotlib.pyplot as plt
import numpy as np


def parse_execution_time_line(line):
    """
    Parse a line containing "Container execution time" and "[imgresize-tasksvc-imgresize-"
    and extract the execution time in milliseconds.
    Expected format: ... XXXX.XXms
    Returns the time in milliseconds as a float, or None if parsing fails.
    """
    if "Container execution time" not in line:
        return None
    if "[imgresize-tasksvc-imgresize-" not in line:
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


def extract_execution_times(dir_path):
    """
    Search all log files in dir_path/sigmaos-node-logs for lines containing
    "Container execution time" and "[imgresize-tasksvc-imgresize-"
    and extract the execution times.
    Returns a list of execution times in milliseconds.
    """
    log_dir = os.path.join(dir_path, "sigmaos-node-logs")

    if not os.path.isdir(log_dir):
        print(f"Error: Directory {log_dir} does not exist", file=sys.stderr)
        return []

    log_files = glob.glob(os.path.join(log_dir, "*"))

    if not log_files:
        print(f"Error: No log files found in {log_dir}", file=sys.stderr)
        return []

    execution_times = []

    for log_file in sorted(log_files):
        # Skip directories
        if os.path.isdir(log_file):
            continue

        try:
            with open(log_file, 'r') as f:
                for line in f:
                    time_ms = parse_execution_time_line(line)
                    if time_ms is not None:
                        execution_times.append(time_ms)
        except Exception as e:
            print(f"Warning: Could not read {log_file}: {e}", file=sys.stderr)

    return execution_times


def main():
    parser = argparse.ArgumentParser(
        description="Create bar graph comparing container execution times for initscript vs no-initscript"
    )
    parser.add_argument(
        "--noinitscript_dir",
        required=True,
        help="Path to benchmark output directory without initscript"
    )
    parser.add_argument(
        "--initscript_dir",
        required=True,
        help="Path to benchmark output directory with initscript"
    )
    parser.add_argument(
        "--output",
        default="imgresize-cost-writeout.png",
        help="Output filename for the graph (default: imgresize-cost-writeout.png)"
    )

    args = parser.parse_args()

    # Extract execution times for both configurations
    times_noinitscript = extract_execution_times(args.noinitscript_dir)
    times_initscript = extract_execution_times(args.initscript_dir)

    if not times_noinitscript:
        print(f"Warning: No execution times found in {args.noinitscript_dir}", file=sys.stderr)
    if not times_initscript:
        print(f"Warning: No execution times found in {args.initscript_dir}", file=sys.stderr)

    if not times_noinitscript and not times_initscript:
        print("Error: No data found in either directory", file=sys.stderr)
        sys.exit(1)

    # Calculate averages
    avg_noinitscript = np.mean(times_noinitscript) if times_noinitscript else 0
    avg_initscript = np.mean(times_initscript) if times_initscript else 0

    # Prepare data for plotting
    labels = ['Without InitScript', 'With InitScript']
    averages = [avg_noinitscript, avg_initscript]
    colors = ['steelblue', 'coral']

    # Create bar graph
    fig, ax = plt.subplots(figsize=(6.4, 2.4))
    bars = ax.bar(labels, averages, color=colors)

    # Customize the plot
    ax.set_ylabel('Execution Time (ms)', fontsize=12)
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

    # Without InitScript stats
    if times_noinitscript:
        std_noinitscript = np.std(times_noinitscript)
        median_noinitscript = np.median(times_noinitscript)
        p90_noinitscript = np.percentile(times_noinitscript, 90)
        max_noinitscript = np.max(times_noinitscript)
        print(f"Without InitScript: {len(times_noinitscript)} samples")
        print(f"  Avg:    {avg_noinitscript:.2f}ms")
        print(f"  Median: {median_noinitscript:.2f}ms")
        print(f"  StdDev: {std_noinitscript:.2f}ms")
        print(f"  90th percentile: {p90_noinitscript:.2f}ms")
        print(f"  Max:    {max_noinitscript:.2f}ms")
    else:
        print("Without InitScript: No data")

    print()

    # With InitScript stats
    if times_initscript:
        std_initscript = np.std(times_initscript)
        median_initscript = np.median(times_initscript)
        p90_initscript = np.percentile(times_initscript, 90)
        max_initscript = np.max(times_initscript)
        print(f"With InitScript:    {len(times_initscript)} samples")
        print(f"  Avg:    {avg_initscript:.2f}ms")
        print(f"  Median: {median_initscript:.2f}ms")
        print(f"  StdDev: {std_initscript:.2f}ms")
        print(f"  90th percentile: {p90_initscript:.2f}ms")
        print(f"  Max:    {max_initscript:.2f}ms")
    else:
        print("With InitScript: No data")

    print()

    # Comparison
    if avg_noinitscript > 0 and avg_initscript > 0:
        diff = avg_noinitscript - avg_initscript
        pct = (diff / avg_noinitscript) * 100
        print(f"Difference (avg):    {diff:.2f}ms ({pct:.1f}%)")


if __name__ == "__main__":
    main()

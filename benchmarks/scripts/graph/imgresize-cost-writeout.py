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
        description="Create bar graph comparing container execution times for cosandbox vs no-cosandbox"
    )
    parser.add_argument(
        "--nocosandbox_dir",
        required=True,
        help="Path to benchmark output directory without cosandbox"
    )
    parser.add_argument(
        "--cosandbox_dir",
        required=True,
        help="Path to benchmark output directory with cosandbox"
    )
    parser.add_argument(
        "--output",
        default="imgresize-cost-writeout.png",
        help="Output filename for the graph (default: imgresize-cost-writeout.png)"
    )

    args = parser.parse_args()

    # Extract execution times for both configurations
    times_nocosandbox = extract_execution_times(args.nocosandbox_dir)
    times_cosandbox = extract_execution_times(args.cosandbox_dir)

    if not times_nocosandbox:
        print(f"Warning: No execution times found in {args.nocosandbox_dir}", file=sys.stderr)
    if not times_cosandbox:
        print(f"Warning: No execution times found in {args.cosandbox_dir}", file=sys.stderr)

    if not times_nocosandbox and not times_cosandbox:
        print("Error: No data found in either directory", file=sys.stderr)
        sys.exit(1)

    # Calculate averages
    avg_nocosandbox = np.mean(times_nocosandbox) if times_nocosandbox else 0
    avg_cosandbox = np.mean(times_cosandbox) if times_cosandbox else 0

    # Prepare data for plotting
    labels = ['Without co-sandbox', 'With co-sandbox']
    averages = [avg_nocosandbox, avg_cosandbox]
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

    # Without CoSandbox stats
    if times_nocosandbox:
        std_nocosandbox = np.std(times_nocosandbox)
        median_nocosandbox = np.median(times_nocosandbox)
        p90_nocosandbox = np.percentile(times_nocosandbox, 90)
        max_nocosandbox = np.max(times_nocosandbox)
        print(f"Without CoSandbox: {len(times_nocosandbox)} samples")
        print(f"  Avg:    {avg_nocosandbox:.2f}ms")
        print(f"  Median: {median_nocosandbox:.2f}ms")
        print(f"  StdDev: {std_nocosandbox:.2f}ms")
        print(f"  90th percentile: {p90_nocosandbox:.2f}ms")
        print(f"  Max:    {max_nocosandbox:.2f}ms")
    else:
        print("Without CoSandbox: No data")

    print()

    # With CoSandbox stats
    if times_cosandbox:
        std_cosandbox = np.std(times_cosandbox)
        median_cosandbox = np.median(times_cosandbox)
        p90_cosandbox = np.percentile(times_cosandbox, 90)
        max_cosandbox = np.max(times_cosandbox)
        print(f"With CoSandbox:    {len(times_cosandbox)} samples")
        print(f"  Avg:    {avg_cosandbox:.2f}ms")
        print(f"  Median: {median_cosandbox:.2f}ms")
        print(f"  StdDev: {std_cosandbox:.2f}ms")
        print(f"  90th percentile: {p90_cosandbox:.2f}ms")
        print(f"  Max:    {max_cosandbox:.2f}ms")
    else:
        print("With CoSandbox: No data")

    print()

    # Comparison
    if avg_nocosandbox > 0 and avg_cosandbox > 0:
        diff = avg_nocosandbox - avg_cosandbox
        pct = (diff / avg_nocosandbox) * 100
        print(f"Difference (avg):    {diff:.2f}ms ({pct:.1f}%)")


if __name__ == "__main__":
    main()

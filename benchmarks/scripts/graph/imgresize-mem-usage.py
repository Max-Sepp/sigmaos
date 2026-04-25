#!/usr/bin/env python3

import argparse
import glob
import os
import re
import sys
import matplotlib.pyplot as plt
import numpy as np


def parse_pss_line(line):
    """
    Parse a line containing " PSS:" and extract the memory value in KB.
    Expected format: ... PSS: ... XXXXKB
    Returns tuple of (memory_kb, has_cosandbox) or None if parsing fails.
    """
    if " PSS:" not in line:
        return None

    # Extract the last word
    last_word = line.strip().split()[-1]

    # Parse the memory value (expecting format like "1234KB")
    match = re.match(r'(\d+)KB', last_word)
    if not match:
        return None

    memory_kb = int(match.group(1))
    has_cosandbox = "CoSandbox" in line

    return (memory_kb, has_cosandbox)


def extract_pss_values(dir_path):
    """
    Search all log files in dir_path/sigmaos-node-logs for lines containing " PSS:"
    and extract memory values.
    Returns two lists: (cosandbox_pss, no_cosandbox_pss) in KB.
    """
    log_dir = os.path.join(dir_path, "sigmaos-node-logs")

    if not os.path.isdir(log_dir):
        print(f"Error: Directory {log_dir} does not exist", file=sys.stderr)
        return [], []

    log_files = glob.glob(os.path.join(log_dir, "*"))

    if not log_files:
        print(f"Error: No log files found in {log_dir}", file=sys.stderr)
        return [], []

    cosandbox_pss = []
    no_cosandbox_pss = []

    for log_file in sorted(log_files):
        # Skip directories
        if os.path.isdir(log_file):
            continue

        try:
            with open(log_file, 'r') as f:
                for line in f:
                    result = parse_pss_line(line)
                    if result is not None:
                        memory_kb, has_cosandbox = result
                        if has_cosandbox:
                            cosandbox_pss.append(memory_kb)
                        else:
                            no_cosandbox_pss.append(memory_kb)
        except Exception as e:
            print(f"Warning: Could not read {log_file}: {e}", file=sys.stderr)

    return cosandbox_pss, no_cosandbox_pss


def main():
    parser = argparse.ArgumentParser(
        description="Create bar graph comparing PSS memory usage for CoSandbox vs non-CoSandbox processes"
    )
    parser.add_argument(
        "--input_dir",
        required=True,
        help="Path to benchmark output directory"
    )
    parser.add_argument(
        "--output",
        default="imgresize-mem-usage.png",
        help="Output filename for the graph (default: imgresize-mem-usage.png)"
    )

    args = parser.parse_args()

    # Extract PSS values
    cosandbox_pss, no_cosandbox_pss = extract_pss_values(args.input_dir)

    if not cosandbox_pss:
        print(f"Warning: No CoSandbox PSS values found in {args.input_dir}", file=sys.stderr)
    if not no_cosandbox_pss:
        print(f"Warning: No non-CoSandbox PSS values found in {args.input_dir}", file=sys.stderr)

    if not cosandbox_pss and not no_cosandbox_pss:
        print("Error: No PSS data found", file=sys.stderr)
        sys.exit(1)

    # Calculate averages in KB, then convert to MB
    avg_cosandbox_kb = np.mean(cosandbox_pss) if cosandbox_pss else 0
    avg_no_cosandbox_kb = np.mean(no_cosandbox_pss) if no_cosandbox_pss else 0

    # Convert to MB
    avg_cosandbox = avg_cosandbox_kb / 1024
    avg_no_cosandbox = avg_no_cosandbox_kb / 1024

    # Prepare data for plotting
    labels = ['No co-sandbox', 'co-sandbox']
    averages = [avg_no_cosandbox, avg_cosandbox]
    colors = ['steelblue', 'coral']

    # Create bar graph
    fig, ax = plt.subplots(figsize=(6.4, 2.4))
    bars = ax.bar(labels, averages, color=colors)

    # Customize the plot
    ax.set_ylabel('Memory Usage (MB)', fontsize=12)
    ax.grid(axis='y', alpha=0.3, linestyle='--')

    # Add value labels on top of bars
    for bar in bars:
        height = bar.get_height()
        if height > 0:
            ax.text(bar.get_x() + bar.get_width()/2., height,
                   f'{height:.1f}MB',
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
    if no_cosandbox_pss:
        std_no_cosandbox = np.std(no_cosandbox_pss) / 1024
        median_no_cosandbox = np.median(no_cosandbox_pss) / 1024
        min_no_cosandbox = np.min(no_cosandbox_pss) / 1024
        max_no_cosandbox = np.max(no_cosandbox_pss) / 1024
        print(f"Without CoSandbox: {len(no_cosandbox_pss)} samples")
        print(f"  Avg:    {avg_no_cosandbox:.1f}MB")
        print(f"  Median: {median_no_cosandbox:.1f}MB")
        print(f"  StdDev: {std_no_cosandbox:.1f}MB")
        print(f"  Min:    {min_no_cosandbox:.1f}MB")
        print(f"  Max:    {max_no_cosandbox:.1f}MB")
    else:
        print("Without CoSandbox: No data")

    print()

    # With CoSandbox stats
    if cosandbox_pss:
        std_cosandbox = np.std(cosandbox_pss) / 1024
        median_cosandbox = np.median(cosandbox_pss) / 1024
        min_cosandbox = np.min(cosandbox_pss) / 1024
        max_cosandbox = np.max(cosandbox_pss) / 1024
        print(f"With CoSandbox:    {len(cosandbox_pss)} samples")
        print(f"  Avg:    {avg_cosandbox:.1f}MB")
        print(f"  Median: {median_cosandbox:.1f}MB")
        print(f"  StdDev: {std_cosandbox:.1f}MB")
        print(f"  Min:    {min_cosandbox:.1f}MB")
        print(f"  Max:    {max_cosandbox:.1f}MB")
    else:
        print("With CoSandbox: No data")

    print()

    # Comparison
    if avg_no_cosandbox > 0 and avg_cosandbox > 0:
        diff = avg_cosandbox - avg_no_cosandbox
        pct = (diff / avg_no_cosandbox) * 100
        print(f"Difference (avg):    {diff:.1f}MB ({pct:.1f}%)")


if __name__ == "__main__":
    main()

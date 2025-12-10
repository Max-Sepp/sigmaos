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
    Returns tuple of (memory_kb, has_bootscript) or None if parsing fails.
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
    has_bootscript = "BootScript" in line

    return (memory_kb, has_bootscript)


def extract_pss_values(dir_path):
    """
    Search all log files in dir_path/sigmaos-node-logs for lines containing " PSS:"
    and extract memory values.
    Returns two lists: (bootscript_pss, no_bootscript_pss) in KB.
    """
    log_dir = os.path.join(dir_path, "sigmaos-node-logs")

    if not os.path.isdir(log_dir):
        print(f"Error: Directory {log_dir} does not exist", file=sys.stderr)
        return [], []

    log_files = glob.glob(os.path.join(log_dir, "*"))

    if not log_files:
        print(f"Error: No log files found in {log_dir}", file=sys.stderr)
        return [], []

    bootscript_pss = []
    no_bootscript_pss = []

    for log_file in sorted(log_files):
        # Skip directories
        if os.path.isdir(log_file):
            continue

        try:
            with open(log_file, 'r') as f:
                for line in f:
                    result = parse_pss_line(line)
                    if result is not None:
                        memory_kb, has_bootscript = result
                        if has_bootscript:
                            bootscript_pss.append(memory_kb)
                        else:
                            no_bootscript_pss.append(memory_kb)
        except Exception as e:
            print(f"Warning: Could not read {log_file}: {e}", file=sys.stderr)

    return bootscript_pss, no_bootscript_pss


def main():
    parser = argparse.ArgumentParser(
        description="Create bar graph comparing PSS memory usage for BootScript vs non-BootScript processes"
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
    bootscript_pss, no_bootscript_pss = extract_pss_values(args.input_dir)

    if not bootscript_pss:
        print(f"Warning: No BootScript PSS values found in {args.input_dir}", file=sys.stderr)
    if not no_bootscript_pss:
        print(f"Warning: No non-BootScript PSS values found in {args.input_dir}", file=sys.stderr)

    if not bootscript_pss and not no_bootscript_pss:
        print("Error: No PSS data found", file=sys.stderr)
        sys.exit(1)

    # Calculate averages in KB, then convert to MB
    avg_bootscript_kb = np.mean(bootscript_pss) if bootscript_pss else 0
    avg_no_bootscript_kb = np.mean(no_bootscript_pss) if no_bootscript_pss else 0

    # Convert to MB
    avg_bootscript = avg_bootscript_kb / 1024
    avg_no_bootscript = avg_no_bootscript_kb / 1024

    # Prepare data for plotting
    labels = ['Without InitScript', 'With InitScript']
    averages = [avg_no_bootscript, avg_bootscript]
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

    # Without BootScript stats
    if no_bootscript_pss:
        std_no_bootscript = np.std(no_bootscript_pss) / 1024
        median_no_bootscript = np.median(no_bootscript_pss) / 1024
        min_no_bootscript = np.min(no_bootscript_pss) / 1024
        max_no_bootscript = np.max(no_bootscript_pss) / 1024
        print(f"Without BootScript: {len(no_bootscript_pss)} samples")
        print(f"  Avg:    {avg_no_bootscript:.1f}MB")
        print(f"  Median: {median_no_bootscript:.1f}MB")
        print(f"  StdDev: {std_no_bootscript:.1f}MB")
        print(f"  Min:    {min_no_bootscript:.1f}MB")
        print(f"  Max:    {max_no_bootscript:.1f}MB")
    else:
        print("Without BootScript: No data")

    print()

    # With BootScript stats
    if bootscript_pss:
        std_bootscript = np.std(bootscript_pss) / 1024
        median_bootscript = np.median(bootscript_pss) / 1024
        min_bootscript = np.min(bootscript_pss) / 1024
        max_bootscript = np.max(bootscript_pss) / 1024
        print(f"With BootScript:    {len(bootscript_pss)} samples")
        print(f"  Avg:    {avg_bootscript:.1f}MB")
        print(f"  Median: {median_bootscript:.1f}MB")
        print(f"  StdDev: {std_bootscript:.1f}MB")
        print(f"  Min:    {min_bootscript:.1f}MB")
        print(f"  Max:    {max_bootscript:.1f}MB")
    else:
        print("With BootScript: No data")

    print()

    # Comparison
    if avg_no_bootscript > 0 and avg_bootscript > 0:
        diff = avg_bootscript - avg_no_bootscript
        pct = (diff / avg_no_bootscript) * 100
        print(f"Difference (avg):    {diff:.1f}MB ({pct:.1f}%)")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3

import argparse
import glob
import os
import re
import sys
import numpy as np


def parse_time_line(line):
    """
    Parse a line containing "ALWAYS Time " and extract the time value in milliseconds.
    Expected format: ... ALWAYS Time ... XXXms
    Returns the time in milliseconds or None if parsing fails.
    """
    if "ALWAYS Time " not in line:
        return None

    # Extract the last word
    last_word = line.strip().split()[-1]

    # Parse the time value (expecting format like "123ms" or "123.45ms")
    match = re.match(r'(\d+(?:\.\d+)?)(ms|µs|us|s)', last_word)
    if not match:
        return None

    timing_value = float(match.group(1))
    timing_unit = match.group(2)

    # Convert to milliseconds
    if timing_unit in ['µs', 'us']:
        return timing_value / 1000.0
    elif timing_unit == 's':
        return timing_value * 1000.0
    else:  # ms
        return timing_value


def extract_time_values(dir_path):
    """
    Search all log files in dir_path/sigmaos-node-logs for lines containing "ALWAYS Time "
    and extract time values.
    Returns a dict mapping operation names to lists of time values in milliseconds.
    """
    log_dir = os.path.join(dir_path, "sigmaos-node-logs")

    if not os.path.isdir(log_dir):
        print(f"Error: Directory {log_dir} does not exist", file=sys.stderr)
        return {}

    log_files = glob.glob(os.path.join(log_dir, "*"))

    if not log_files:
        print(f"Error: No log files found in {log_dir}", file=sys.stderr)
        return {}

    # Dict mapping (operation name, full line after "Time") to list of times
    # We use the full line after "Time" as the key to deduplicate
    time_entries = {}

    for log_file in sorted(log_files):
        # Skip directories
        if os.path.isdir(log_file):
            continue

        try:
            with open(log_file, 'r') as f:
                for line in f:
                    if "ALWAYS Time " not in line:
                        continue

                    time_ms = parse_time_line(line)
                    if time_ms is not None:
                        # Special case: if line contains "e2e resize", use that as the key
                        if "e2e resize" in line:
                            operation_text = "e2e resize"
                        else:
                            # Extract everything after "ALWAYS Time " but before the time value
                            # Remove the last word (which is the time value)
                            match = re.search(r'ALWAYS Time (.+)', line)
                            if match:
                                full_text = match.group(1).strip()
                                # Remove the last word (the time value like "45ms")
                                words = full_text.split()
                                if len(words) > 1:
                                    operation_text = ' '.join(words[:-1])
                                else:
                                    operation_text = full_text
                            else:
                                continue

                        # Use the operation text (without time value) as the key for deduplication
                        if operation_text not in time_entries:
                            time_entries[operation_text] = []
                        time_entries[operation_text].append(time_ms)
        except Exception as e:
            print(f"Warning: Could not read {log_file}: {e}", file=sys.stderr)

    return time_entries


def main():
    parser = argparse.ArgumentParser(
        description="Extract and average time values from lines containing 'ALWAYS Time '"
    )
    parser.add_argument(
        "--input_dir",
        required=True,
        help="Path to benchmark output directory"
    )

    args = parser.parse_args()

    # Extract time values
    time_values = extract_time_values(args.input_dir)

    if not time_values:
        print(f"Error: No 'ALWAYS Time ' lines found in {args.input_dir}", file=sys.stderr)
        sys.exit(1)

    # Print average times
    print("Time Breakdown (Averages):")
    print("=" * 80)

    # Sort by entry text for consistent output
    for entry_text in sorted(time_values.keys()):
        times = time_values[entry_text]
        avg = np.mean(times)
        print(f"{entry_text} {avg:.2f}ms")

    print("=" * 80)


if __name__ == "__main__":
    main()

package mem

import (
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	db "sigmaos/debug"
	"sigmaos/proc"
)

var totalMem proc.Tmem = 0

func getMem(pat string) proc.Tmem {
	b, err := ioutil.ReadFile("/proc/meminfo")
	if err != nil {
		db.DFatalf("Can't read /proc/meminfo: %v", err)
	}
	lines := strings.Split(string(b), "\n")
	for _, l := range lines {
		if strings.Contains(l, pat) {
			s := strings.Split(l, " ")
			kbStr := s[len(s)-2]
			kb, err := strconv.Atoi(kbStr)
			if err != nil {
				db.DFatalf("Couldn't convert MemTotal: %v", err)
			}
			return proc.Tmem(kb / 1024)
		}
	}
	db.DFatalf("Couldn't find total mem")
	return 0
}

// Total amount of memory, in MB.
func GetTotalMem() proc.Tmem {
	if totalMem == 0 {
		totalMem = getMem("MemTotal")
	}
	return totalMem
}

// Available amount of memory, in MB.
func GetAvailableMem() proc.Tmem {
	return getMem("MemAvailable")
}

// getPSS reads the PSS (Proportional Set Size) for a given Linux PID in KB.
// It reads from /proc/<pid>/smaps_rollup if available, otherwise falls back to /proc/<pid>/smaps.
func getPSS(linuxPID int) (uint64, error) {
	// Try smaps_rollup first (faster, available in kernel 4.14+)
	smapsRollupPath := fmt.Sprintf("/proc/%d/smaps_rollup", linuxPID)
	if data, err := ioutil.ReadFile(smapsRollupPath); err == nil {
		return parsePSSFromSmaps(string(data))
	}

	// Fall back to smaps
	smapsPath := fmt.Sprintf("/proc/%d/smaps", linuxPID)
	data, err := ioutil.ReadFile(smapsPath)
	if err != nil {
		return 0, err
	}
	return parsePSSFromSmaps(string(data))
}

// parsePSSFromSmaps parses the Pss field from smaps or smaps_rollup content.
func parsePSSFromSmaps(content string) (uint64, error) {
	lines := strings.Split(content, "\n")
	var totalPSS uint64
	for _, line := range lines {
		if strings.HasPrefix(line, "Pss:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				pss, err := strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					return 0, fmt.Errorf("failed to parse PSS value: %v", err)
				}
				totalPSS += pss
			}
		}
	}
	return totalPSS, nil
}

// getChildPIDs returns all child PIDs of the given Linux PID.
func getChildPIDs(linuxPID int) ([]int, error) {
	procDir := "/proc"
	entries, err := ioutil.ReadDir(procDir)
	if err != nil {
		return nil, err
	}

	var children []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Check if directory name is a number (PID)
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		// Read the stat file to get PPID
		statPath := fmt.Sprintf("/proc/%d/stat", pid)
		statData, err := ioutil.ReadFile(statPath)
		if err != nil {
			continue
		}

		// Parse PPID from stat file
		// Format: pid (comm) state ppid ...
		statStr := string(statData)
		// Find the last ')' to handle process names with spaces/parentheses
		lastParen := strings.LastIndex(statStr, ")")
		if lastParen == -1 {
			continue
		}
		fields := strings.Fields(statStr[lastParen+1:])
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		if ppid == linuxPID {
			children = append(children, pid)
		}
	}
	return children, nil
}

// GetAggregatePSS calculates the aggregate PSS size (in KB) for a process and all of its children.
// It recursively finds all descendant processes and sums their PSS values.
func GetAggregatePSS(linuxPID int) (proc.Tmem, error) {
	// Check if the process exists
	procPath := fmt.Sprintf("/proc/%d", linuxPID)
	if _, err := os.Stat(procPath); os.IsNotExist(err) {
		return 0, fmt.Errorf("process %d does not exist", linuxPID)
	}

	var totalPSSKB uint64

	// Get PSS for the current process
	pss, err := getPSS(linuxPID)
	if err != nil {
		db.DPrintf(db.ALWAYS, "Warning: failed to get PSS for PID %d: %v", linuxPID, err)
	} else {
		totalPSSKB += pss
	}

	// Recursively get PSS for all children
	var toProcess []int
	toProcess = append(toProcess, linuxPID)
	visited := make(map[int]bool)
	visited[linuxPID] = true

	for len(toProcess) > 0 {
		currentPID := toProcess[0]
		toProcess = toProcess[1:]

		children, err := getChildPIDs(currentPID)
		if err != nil {
			continue
		}

		for _, childPID := range children {
			if visited[childPID] {
				continue
			}
			visited[childPID] = true

			childPSS, err := getPSS(childPID)
			if err != nil {
				db.DPrintf(db.ALWAYS, "Warning: failed to get PSS for child PID %d: %v", childPID, err)
			} else {
				totalPSSKB += childPSS
			}

			toProcess = append(toProcess, childPID)
		}
	}

	// Convert KB to MB
	return proc.Tmem(totalPSSKB), nil
}

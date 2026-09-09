//go:build !windows

package doctor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func defaultProcessLister() ([]ProcessInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, nil
	}
	var procs []ProcessInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		statRaw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(statRaw))
		if len(fields) >= 4 {
			ppid, _ := strconv.Atoi(fields[3])
			name := strings.Trim(fields[1], "()")
			procs = append(procs, ProcessInfo{
				Pid:       pid,
				ParentPid: ppid,
				Name:      name,
			})
		}
	}
	return procs, nil
}

//go:build windows

package doctor

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func defaultProcessLister() ([]ProcessInfo, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil, err
	}

	var procs []ProcessInfo
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		procs = append(procs, ProcessInfo{
			Pid:       int(entry.ProcessID),
			ParentPid: int(entry.ParentProcessID),
			Name:      name,
		})
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return procs, nil
}

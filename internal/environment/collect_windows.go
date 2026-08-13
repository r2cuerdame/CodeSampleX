//go:build windows

package environment

import "golang.org/x/sys/windows"

// osVersionBucket returns "11" or "10" — build 22000 marked the Windows 11
// cut; finer detail is deliberately not collected (goal.md §7.3 bucketing).
func osVersionBucket() string {
	major, _, build := windows.RtlGetNtVersionNumbers()
	if major >= 10 && build >= 22000 {
		return "11"
	}
	if major > 0 {
		if major == 10 {
			return "10"
		}
		return "10"
	}
	return ""
}

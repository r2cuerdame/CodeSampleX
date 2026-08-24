//go:build windows

package evidence

import "os"

func processSignal(_ *os.ProcessState) string { return "" }

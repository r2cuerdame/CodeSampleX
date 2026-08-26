//go:build !windows

package defender

import "context"

// Scan has no answer off Windows. It returns ErrUnavailable rather than an
// empty (and therefore clean-looking) slice, so a Linux or macOS caller cannot
// record "Defender found nothing" for a machine that has no Defender.
func Scan(context.Context, ...string) ([]Verdict, error) { return nil, ErrUnavailable }

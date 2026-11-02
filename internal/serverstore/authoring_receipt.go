package serverstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
)

// AuthoringJobReceiptRefs preserves the existing bounded reader's SHA256
// fingerprints: ASCII JSON [package,symbol,axis], then jobRef+"\n"+UTC expiry.
// These are correlation fingerprints, never a bearer or server-issued work ID.
func AuthoringJobReceiptRefs(purl, symbol, axis string, expires time.Time) (string, string) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode([]string{purl, symbol, NormalizeAuthoringAxis(axis)})
	var ascii strings.Builder
	for _, r := range strings.TrimSuffix(encoded.String(), "\n") {
		switch {
		case r < 127:
			ascii.WriteRune(r)
		case r <= 0xffff:
			fmt.Fprintf(&ascii, "\\u%04x", r)
		default:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&ascii, "\\u%04x\\u%04x", hi, lo)
		}
	}
	jobSum := sha256.Sum256([]byte(ascii.String()))
	jobRef := hex.EncodeToString(jobSum[:])
	claimSum := sha256.Sum256([]byte(jobRef + "\n" + expires.UTC().Format(time.RFC3339)))
	claimRef := hex.EncodeToString(claimSum[:])
	return jobRef, claimRef
}

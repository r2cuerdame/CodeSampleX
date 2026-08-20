// Package identity holds the peer's ed25519 keypair and the secret seed
// behind the rotating pseudonymous evidence IDs (goal.md §8.6, plan C10).
// The seed never leaves the machine; only HMAC-derived, epoch-scoped
// buckets appear in uploads, so the server can dedupe within an epoch
// but cannot link across epochs or recover paths.
package identity

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const anonSeedLen = 32

// Identity is the loaded local identity. The zero value is unusable;
// obtain one via LoadOrCreate.
type Identity struct {
	priv     ed25519.PrivateKey
	anonSeed []byte
}

type identityFile struct {
	SchemaVersion int    `json:"schemaVersion"`
	Ed25519Priv   string `json:"ed25519Priv"`
	AnonSeed      string `json:"anonSeed"`
}

// LoadOrCreate reads home/identity.json, generating and persisting a new
// identity (file mode 0600) if none exists.
func LoadOrCreate(home string) (*Identity, error) {
	path := filepath.Join(home, "identity.json")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return create(home, path)
	}
	// loadWhenComplete, not loadExisting: the file EXISTING is not the file
	// being finished. O_EXCL makes it appear atomically and its contents
	// arrive a moment later, so a caller whose Stat lands inside that window
	// reads zero bytes and fails with "unexpected end of JSON input".
	//
	// The retry already existed for the caller that loses the O_EXCL race.
	// This path -- the far more common one, where the file was simply already
	// there -- went straight to a bare read and had no such cover.
	return loadWhenComplete(path)
}

// loadWhenComplete reads an identity another process is in the middle of
// creating.
//
// Electing one writer is not enough on its own: O_EXCL makes the FILE
// appear atomically, but its CONTENTS arrive a moment later, and a loser
// that read in that window got "unexpected end of JSON input" and returned
// no identity at all. Linux CI caught it on the first concurrent run; the
// window is microseconds and a Windows laptop never hit it.
//
// The file is ~200 bytes written by one call, so a few short retries cover
// it. A file still unreadable after that is a real problem -- a crash
// during that same window would leave one -- and is reported rather than
// silently replaced, because replacing it would throw away the private key
// this machine's whole history is signed with.
func loadWhenComplete(path string) (*Identity, error) {
	var err error
	for attempt := range 50 {
		var id *Identity
		if id, err = loadExisting(path); err == nil {
			return id, nil
		}
		if attempt < 49 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	return nil, err
}

// loadExisting parses an identity file that is already on disk.
func loadExisting(path string) (*Identity, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identity: load: %w", err)
	}
	var f identityFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("identity: parse identity.json: %w", err)
	}
	priv, err := base64.StdEncoding.DecodeString(f.Ed25519Priv)
	if err != nil {
		return nil, fmt.Errorf("identity: decode ed25519Priv: %w", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("identity: ed25519Priv length %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	seed, err := base64.StdEncoding.DecodeString(f.AnonSeed)
	if err != nil {
		return nil, fmt.Errorf("identity: decode anonSeed: %w", err)
	}
	if len(seed) != anonSeedLen {
		return nil, fmt.Errorf("identity: anonSeed length %d, want %d", len(seed), anonSeedLen)
	}
	return &Identity{priv: ed25519.PrivateKey(priv), anonSeed: seed}, nil
}

func create(home, path string) (*Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: generate key: %w", err)
	}
	seed := make([]byte, anonSeedLen)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("identity: generate seed: %w", err)
	}
	f := identityFile{
		SchemaVersion: 1,
		Ed25519Priv:   base64.StdEncoding.EncodeToString(priv),
		AnonSeed:      base64.StdEncoding.EncodeToString(seed),
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("identity: marshal: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, fmt.Errorf("identity: save: %w", err)
	}
	// Create EXCLUSIVELY. os.WriteFile truncates, so every process that
	// started at the same moment and found no identity generated one and
	// wrote over the others: eight concurrent callers on a fresh home
	// produced eight different peer IDs, seven of them discarded on disk
	// while each caller kept using the key it had made in memory.
	//
	// The daemon, the MCP server and a CLI command all start together on a
	// first run, so this is the ordinary path rather than an unlucky one.
	// The anonSeed is what every rotating evidence ID is derived from, so
	// disagreeing about it makes one machine count as several independent
	// peers — the exact inflation the server side was just fixed to stop,
	// arriving from the client instead.
	//
	// Whoever creates the file first wins; everyone else reads it.
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return loadWhenComplete(path)
	}
	if err != nil {
		return nil, fmt.Errorf("identity: save: %w", err)
	}
	if _, err := fh.Write(raw); err != nil {
		fh.Close()
		return nil, fmt.Errorf("identity: save: %w", err)
	}
	if err := fh.Close(); err != nil {
		return nil, fmt.Errorf("identity: save: %w", err)
	}
	return &Identity{priv: priv, anonSeed: seed}, nil
}

// PeerID is the persistent public identity: "ed25519:" + hex(sha256(pubkey))[:16].
func (id *Identity) PeerID() string {
	sum := sha256.Sum256(id.priv.Public().(ed25519.PublicKey))
	return "ed25519:" + hex.EncodeToString(sum[:])[:16]
}

// PubkeyB64 returns the base64 ed25519 public key (receipt peerPubkey field).
func (id *Identity) PubkeyB64() string {
	return base64.StdEncoding.EncodeToString(id.priv.Public().(ed25519.PublicKey))
}

// Sign returns the base64 ed25519 signature over msg (canonical JSON).
func (id *Identity) Sign(msg []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(id.priv, msg))
}

// AnonID derives the rotating anonymous evidence ID for one daily epoch
// ("2026-08-13"): hex(HMAC-SHA256(seed, "anon|"+epochDay))[:16].
func (id *Identity) AnonID(epochDay string) string {
	return id.derive("anon|"+epochDay, 16)
}

// ProjectBucket derives the rotating per-project dedup bucket for one
// monthly epoch ("2026-08"): hex(HMAC-SHA256(seed, "proj|"+path+"|"+month))[:12].
// The absolute path stays inside the HMAC input; it is never recoverable
// from the 12-hex output.
func (id *Identity) ProjectBucket(projectPath, epochMonth string) string {
	return id.derive("proj|"+projectPath+"|"+epochMonth, 12)
}

func (id *Identity) derive(input string, n int) string {
	mac := hmac.New(sha256.New, id.anonSeed)
	mac.Write([]byte(input))
	return hex.EncodeToString(mac.Sum(nil))[:n]
}

// Verify reports whether sigB64 is a valid ed25519 signature by pubB64
// over msg. Malformed inputs verify as false, never panic.
func Verify(pubB64, sigB64 string, msg []byte) bool {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

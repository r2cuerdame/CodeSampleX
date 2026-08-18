// Package update implements the signed, fail-closed CodeSampleX binary updater.
// It deliberately uses only the Go standard library so the updater does not
// add another executable supply-chain dependency of its own.
package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	ManifestSchema     = 1
	DefaultChannel     = "stable"
	DefaultManifestURL = "https://github.com/r2cuerdame/CodeSampleX/releases/latest/download/csx-update-stable.json"
)

// PublicKeyBase64 is stamped into release binaries from the protected release
// signing secret. Development builds intentionally have no update trust root.
var PublicKeyBase64 string

const maxSignedPayloadBytes = 256 << 10

const maxManifestValidity = 90 * 24 * time.Hour

var canonicalReleaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func IsCanonicalReleaseVersion(v string) bool { return canonicalReleaseVersion.MatchString(v) }

type Envelope struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type Manifest struct {
	Schema            int       `json:"schema"`
	Channel           string    `json:"channel"`
	Sequence          uint64    `json:"sequence"`
	Version           string    `json:"version"`
	PublishedAt       time.Time `json:"publishedAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
	MinUpdaterVersion string    `json:"minUpdaterVersion,omitempty"`
	Assets            []Asset   `json:"assets"`
}

type Asset struct {
	OS                 string `json:"os"`
	Arch               string `json:"arch"`
	URL                string `json:"url"`
	Size               int64  `json:"size"`
	SHA256             string `json:"sha256"`
	MinLauncherVersion string `json:"minLauncherVersion,omitempty"`
}

// VerifyEnvelope authenticates the raw payload before parsing it. Signing the
// exact bytes avoids any dependence on JSON canonicalization rules.
func VerifyEnvelope(raw []byte, publicKey ed25519.PublicKey, now time.Time, channel string) (Manifest, error) {
	var env Envelope
	if len(raw) > maxManifestBytes {
		return Manifest{}, errors.New("update: manifest envelope exceeds size limit")
	}
	if err := decodeStrict(raw, &env); err != nil {
		return Manifest{}, fmt.Errorf("update: decode envelope: %w", err)
	}
	if len(env.Payload) > base64.StdEncoding.EncodedLen(maxSignedPayloadBytes) {
		return Manifest{}, errors.New("update: signed payload exceeds size limit")
	}
	if len(env.Signature) > base64.StdEncoding.EncodedLen(ed25519.SignatureSize) {
		return Manifest{}, errors.New("update: signature exceeds size limit")
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return Manifest{}, fmt.Errorf("update: decode payload: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil {
		return Manifest{}, fmt.Errorf("update: decode signature: %w", err)
	}
	if len(payload) > maxSignedPayloadBytes || len(sig) != ed25519.SignatureSize {
		return Manifest{}, errors.New("update: signed manifest has invalid size")
	}
	if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, payload, sig) {
		return Manifest{}, errors.New("update: manifest signature is invalid")
	}
	var m Manifest
	if err := decodeStrict(payload, &m); err != nil {
		return Manifest{}, fmt.Errorf("update: decode signed manifest: %w", err)
	}
	if m.Schema != ManifestSchema {
		return Manifest{}, fmt.Errorf("update: unsupported manifest schema %d", m.Schema)
	}
	if channel == "" {
		channel = DefaultChannel
	}
	if m.Channel != channel {
		return Manifest{}, fmt.Errorf("update: manifest channel %q does not match %q", m.Channel, channel)
	}
	if m.Sequence == 0 || m.Version == "" || len(m.Assets) == 0 {
		return Manifest{}, errors.New("update: signed manifest is incomplete")
	}
	if !IsCanonicalReleaseVersion(m.Version) {
		return Manifest{}, fmt.Errorf("update: release version %q is not canonical vMAJOR.MINOR.PATCH", m.Version)
	}
	if channel == DefaultChannel && strings.Contains(strings.TrimPrefix(m.Version, "v"), "-") {
		return Manifest{}, fmt.Errorf("update: stable channel refused prerelease %q", m.Version)
	}
	if m.PublishedAt.IsZero() || m.ExpiresAt.IsZero() || !m.ExpiresAt.After(m.PublishedAt) {
		return Manifest{}, errors.New("update: invalid manifest validity window")
	}
	if m.ExpiresAt.Sub(m.PublishedAt) > maxManifestValidity {
		return Manifest{}, errors.New("update: manifest validity exceeds 90 days")
	}
	if m.MinUpdaterVersion != "" && !IsCanonicalReleaseVersion(m.MinUpdaterVersion) {
		return Manifest{}, fmt.Errorf("update: minimum updater version %q is not canonical", m.MinUpdaterVersion)
	}
	if now.Before(m.PublishedAt.Add(-10 * time.Minute)) {
		return Manifest{}, errors.New("update: manifest is dated in the future")
	}
	if !now.Before(m.ExpiresAt) {
		return Manifest{}, errors.New("update: manifest has expired")
	}
	seen := map[string]bool{}
	for _, a := range m.Assets {
		key := a.OS + "/" + a.Arch
		if seen[key] {
			return Manifest{}, fmt.Errorf("update: duplicate asset target %s", key)
		}
		seen[key] = true
		if a.OS == "" || a.Arch == "" || a.URL == "" || a.Size <= 0 || len(a.SHA256) != 64 || !isLowerHex(a.SHA256) {
			return Manifest{}, fmt.Errorf("update: invalid asset for %s", key)
		}
		if err := validateSignedAssetURL(a.URL, m.Version, a.OS, a.Arch); err != nil {
			return Manifest{}, err
		}
		if a.MinLauncherVersion != "" {
			if a.OS != "windows" || !IsCanonicalReleaseVersion(a.MinLauncherVersion) {
				return Manifest{}, fmt.Errorf("update: invalid minimum launcher version for %s", key)
			}
		}
	}
	return m, nil
}

func decodeStrict(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func EmbeddedPublicKey() (ed25519.PublicKey, error) {
	if PublicKeyBase64 == "" {
		return nil, errors.New("update: this development build has no release update trust root")
	}
	raw, err := base64.StdEncoding.DecodeString(PublicKeyBase64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("update: embedded release update trust root is invalid")
	}
	return ed25519.PublicKey(raw), nil
}

func (m Manifest) AssetFor(osName, arch string) (Asset, error) {
	for _, a := range m.Assets {
		if a.OS == osName && a.Arch == arch {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("update: release %s has no asset for %s/%s", m.Version, osName, arch)
}

func CurrentAsset(m Manifest) (Asset, error) { return m.AssetFor(runtime.GOOS, runtime.GOARCH) }

func isLowerHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// CompareVersions compares strict numeric release versions, ignoring one
// leading v. It rejects ranges, aliases, and prerelease text.
func CompareVersions(a, b string) (int, error) {
	parse := func(v string) ([]int, error) {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		if v == "" || strings.ContainsAny(v, "-+") {
			return nil, fmt.Errorf("not a stable numeric version: %q", v)
		}
		parts := strings.Split(v, ".")
		out := make([]int, len(parts))
		for i, p := range parts {
			if p == "" {
				return nil, fmt.Errorf("not a numeric version: %q", v)
			}
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("not a numeric version: %q", v)
			}
			out[i] = n
		}
		return out, nil
	}
	aa, err := parse(a)
	if err != nil {
		return 0, err
	}
	bb, err := parse(b)
	if err != nil {
		return 0, err
	}
	n := len(aa)
	if len(bb) > n {
		n = len(bb)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(aa) {
			av = aa[i]
		}
		if i < len(bb) {
			bv = bb[i]
		}
		if av < bv {
			return -1, nil
		}
		if av > bv {
			return 1, nil
		}
	}
	return 0, nil
}

// Command update_manifest signs the release updater manifest. The private key
// is read only from CSX_UPDATE_SIGNING_KEY_B64 and is never written to disk.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

func main() {
	if len(os.Args) == 4 && os.Args[1] == "guard" {
		if err := guardRelease(os.Args[2], os.Args[3]); err != nil {
			fatal(err.Error())
		}
		fmt.Println("release order verified", os.Args[2])
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "public" {
		key := signingKey()
		fmt.Println(base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)))
		return
	}
	if len(os.Args) == 5 && os.Args[1] == "verify" {
		pubRaw, err := base64.StdEncoding.DecodeString(os.Args[2])
		if err != nil || len(pubRaw) != ed25519.PublicKeySize {
			fatal("invalid verification public key")
		}
		raw, err := os.ReadFile(os.Args[3])
		if err != nil {
			fatal(err.Error())
		}
		m, err := csxupdate.VerifyEnvelope(raw, ed25519.PublicKey(pubRaw), time.Now().UTC(), csxupdate.DefaultChannel)
		if err != nil {
			fatal(err.Error())
		}
		if m.Version != os.Args[4] || len(m.Assets) != 6 {
			fatal("manifest release identity or asset count mismatch")
		}
		fmt.Println("verified", m.Version)
		return
	}
	if len(os.Args) != 6 || os.Args[1] != "sign" {
		fatal("usage: update_manifest public | guard <candidate> <latest> | sign <version> <sequence> <dist-dir> <output> | verify <public-key-b64> <manifest> <version>")
	}
	key := signingKey()
	version := os.Args[2]
	if !csxupdate.IsCanonicalReleaseVersion(version) {
		fatal("version must be canonical vMAJOR.MINOR.PATCH")
	}
	sequence, err := strconv.ParseUint(os.Args[3], 10, 64)
	if err != nil || sequence == 0 {
		fatal("invalid nonzero sequence")
	}
	dist, output := os.Args[4], os.Args[5]
	now := time.Now().UTC().Truncate(time.Second)
	m := csxupdate.Manifest{
		Schema: csxupdate.ManifestSchema, Channel: csxupdate.DefaultChannel,
		Sequence: sequence, Version: version, PublishedAt: now,
		ExpiresAt: now.Add(90 * 24 * time.Hour),
		Assets:    assets(dist, version),
	}
	payload, err := json.Marshal(m)
	if err != nil {
		fatal(err.Error())
	}
	env := csxupdate.Envelope{
		Payload:   base64.StdEncoding.EncodeToString(payload),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload)),
	}
	raw, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		fatal(err.Error())
	}
	raw = append(raw, '\n')
	if _, err := csxupdate.VerifyEnvelope(raw, key.Public().(ed25519.PublicKey), now, csxupdate.DefaultChannel); err != nil {
		fatal("self-verification failed: " + err.Error())
	}
	if err := os.WriteFile(output, raw, 0o644); err != nil {
		fatal(err.Error())
	}
}

func guardRelease(candidate, latest string) error {
	if !csxupdate.IsCanonicalReleaseVersion(candidate) || !csxupdate.IsCanonicalReleaseVersion(latest) {
		return errors.New("candidate and latest release must be canonical vMAJOR.MINOR.PATCH")
	}
	cmp, err := csxupdate.CompareVersions(candidate, latest)
	if err != nil {
		return err
	}
	if cmp < 0 {
		return fmt.Errorf("refusing release %s below current latest %s", candidate, latest)
	}
	return nil
}

func signingKey() ed25519.PrivateKey {
	raw, err := base64.StdEncoding.DecodeString(os.Getenv("CSX_UPDATE_SIGNING_KEY_B64"))
	if err != nil {
		fatal("CSX_UPDATE_SIGNING_KEY_B64 is not valid base64")
	}
	key, err := parseSigningKey(raw)
	if err != nil {
		fatal(err.Error())
	}
	return key
}

func parseSigningKey(raw []byte) (ed25519.PrivateKey, error) {
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		derived := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
		if subtle.ConstantTimeCompare(raw, derived) != 1 {
			return nil, errors.New("64-byte private key has an inconsistent public suffix")
		}
		return derived, nil
	default:
		return nil, errors.New("CSX_UPDATE_SIGNING_KEY_B64 must contain a 32-byte seed or 64-byte private key")
	}
}

func assets(dist, version string) []csxupdate.Asset {
	var out []csxupdate.Asset
	for _, target := range []struct{ os, arch string }{
		{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"},
		{"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"},
	} {
		name := "csx-" + target.os + "-" + target.arch
		if target.os == "windows" {
			name += ".exe"
		}
		raw, err := os.ReadFile(filepath.Join(dist, name))
		if err != nil {
			fatal(err.Error())
		}
		sum := sha256.Sum256(raw)
		out = append(out, csxupdate.Asset{
			OS: target.os, Arch: target.arch,
			URL:  "https://github.com/r2cuerdame/CodeSampleX/releases/download/" + version + "/" + name,
			Size: int64(len(raw)), SHA256: hex.EncodeToString(sum[:]),
		})
		if target.os == "windows" {
			out[len(out)-1].MinLauncherVersion = "v1.0.0"
			// The launcher deliberately does NOT travel here.
			//
			// v0.1.92 put launcherUrl/launcherSize/launcherSha256 in this
			// structure so `csx update` could deliver the launcher.
			// VerifyEnvelope decodes the signed payload with
			// DisallowUnknownFields, so every client built before those
			// fields existed stopped being able to read the manifest at all.
			// Measured against the live manifest with a real v0.1.87 binary:
			//
			//	decode signed manifest: json: unknown field "launcherUrl"
			//
			// An update is the only way to fix a client, and the update is
			// what broke -- so every csx at or below v0.1.91 was frozen,
			// the farm node among them, pinned at v0.1.90 and unable to
			// move.
			//
			// The client half stays: v0.1.92 and later understand these
			// fields if they ever appear. Nothing goes back into this
			// structure until no supported client rejects unknown fields,
			// and that is a measurement rather than a date.
		}
	}
	return out
}

func fatal(msg string) { fmt.Fprintln(os.Stderr, "update_manifest:", msg); os.Exit(1) }

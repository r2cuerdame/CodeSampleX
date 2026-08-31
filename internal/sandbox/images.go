package sandbox

import (
	"fmt"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// This file is the ONE place a verifier image may come from.
//
// The public promise is that a published sample's contract ran in a pinned
// container and can be re-run against the same bytes. A floating tag does
// not keep it. `node:22-alpine` is whatever the registry points at today,
// and on a worker that has run before it is whatever already sits in the
// local cache under that name — docker will not re-check a tag it already
// has. Two honest workers could therefore sign receipts naming the same
// environment for runs of different software, and a re-verification months
// later would silently be a different measurement.
//
// So the alias is kept for humans and the DIGEST is the execution
// authority: every selector returns `<alias>@sha256:<digest>`, docker
// resolves that by content, and a stale local tag is unreachable.
//
// The base and libc live here too, beside the digest they describe. They
// used to sit in a second table keyed by image name, which is exactly the
// kind of pair that drifts: composer:2 is an Alpine image whose tag does
// not say so, and for as long as the base was inferred from the name every
// PHP receipt this project produced claimed glibc for a run on musl.
type verifierImage struct {
	// alias is the readable tag. It names what the image is MEANT to be and
	// is never what runs.
	alias string
	// digest is the immutable multi-platform index digest, as reported by
	// `docker buildx imagetools inspect <alias>`. The index rather than one
	// platform's manifest: nothing passes --platform, so an arm64 worker
	// must still be able to run the same pinned entry, and the architecture
	// it actually ran on is recorded separately in the receipt.
	digest string
	// bucket, distro and libc are what the image ACTUALLY is, established by
	// running it (TestImageBaseMatchesTheRealImage), not read off the name.
	bucket string
	distro string
	libc   string
}

// ref is the only form that may be executed.
func (v verifierImage) ref() string { return v.alias + "@" + v.digest }

// verifierImages is the registry, keyed by alias.
//
// Adding or moving an entry is a deliberate act with a procedure attached
// (docs/adapters.md, "Refreshing a verifier image"): resolve the new digest,
// measure the base, and land it as a reviewed change. Nothing here can be
// updated by a tag moving upstream, because a tag moving upstream cannot
// reach it.
var verifierImages = map[string]verifierImage{
	// Linux verifier lanes.
	"node:22-alpine": {"node:22-alpine", "sha256:c610fcdfb1d5b4740dd70c284ed3cb16bb857e0f7166196e36a5501df7a3aa32", "alpine", "", "musl"},
	// The glibc Node lane, for manifests that declare it.
	//
	// A prebuilt native .node binary is linked against one libc and does not
	// load on the other. Measured on production 2026-09-01: ten npm samples
	// declared environment.libc = "glibc" and every one was verified on
	// node:22-alpine, which is musl. Six died inside two minutes with
	// "Error loading shared library ld-linux-x86-64.so.2: No such file or
	// directory", code ERR_DLOPEN_FAILED — recorded as contract=FAIL, a
	// verdict on the sample, for a mismatch between what its author declared
	// and what the verifier provided.
	//
	// Measured 2026-09-01 by running it: Debian 12, Node v22.23.2, glibc 2.36.
	"node:22":              {"node:22", "sha256:8a34c4ab3ea2c5cd194f07e317b2a8f09461d3c8b05c4e34c8ccd56d56024c4d", "debian", "", "glibc"},
	"oven/bun:1-alpine":    {"oven/bun:1-alpine", "sha256:07235578f79ef8c6f97d94aee7938e76f5cdba5f21ae5dbfdd3d3d38058437eb", "alpine", "", "musl"},
	"denoland/deno:alpine": {"denoland/deno:alpine", "sha256:b49ac52f05c3d8d0da890b6628168e9bfb5721f7bccc00305bb3ad29ed0e40af", "alpine", "", "musl"},
	// The Linux Python lane. Debian rather than Alpine for two reasons, and
	// the second is the bigger one.
	//
	// musl cannot use a manylinux wheel. A project that also publishes
	// musllinux wheels is fine on Alpine — numpy, lxml and pyarrow all
	// install there — but one that publishes manylinux only falls back to an
	// sdist build that this image has no toolchain for. llvmlite is that
	// case, and it is the one nine defect reports named.
	//
	// And every pypi receipt this network holds was musl: 510 of them on
	// 2026-08-30, not one glibc. Honest about what it measured, and the only
	// Python environment it had ever measured, while nearly every Python user
	// is on glibc.
	"python:3.12-slim": {"python:3.12-slim", "sha256:09f7da3bc104798d0afb40bc08d23ab2da20a76130cec1f2ef170848f5d85217", "debian", "", "glibc"},
	"python:3.14-slim": {"python:3.14-slim", "sha256:cae66f2ef0ec51a9891263eeee7f987dacf0a9879e8aa9353d5606e0530619a5", "debian", "", "glibc"},
	// Kept, and no longer selected. 510 receipts name these, and a published
	// sample's promise is that its contract can be re-run against the same
	// bytes — removing the entry would break every one of them.
	"python:3.12-alpine": {"python:3.12-alpine", "sha256:d09d15e60962ca365d1cd544a48773bac9d33f2fb1b00f2aa0deec78ade7dc31", "alpine", "", "musl"},
	// Measured 2026-08-18: Python 3.14.7 on Alpine 3.24 for linux/amd64,
	// with the other published architectures behind the same index digest.
	"python:3.14-alpine": {"python:3.14-alpine", "sha256:05b2b8b732ecd268fee8727a369f936f022d1321b59befd13c30ede22769dcdc", "alpine", "", "musl"},
	"golang:1.26-alpine": {"golang:1.26-alpine", "sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468", "alpine", "", "musl"},
	// The glibc Go lane. 898 golang samples declare glibc — most of the
	// golang corpus — and until this entry existed the only Go image was
	// Alpine, so every one of them either ran on the wrong libc or, once the
	// guard shipped, was refused outright.
	//
	// Measured 2026-09-01 by running it: Debian 13, go1.26.7, glibc 2.41.
	"golang:1.26":   {"golang:1.26", "sha256:e30143be198ab04cf7ba25fba83ab3a692ca584c994aad0bf131fa0eb32dd8c1", "debian", "", "glibc"},
	"rust:1-alpine": {"rust:1-alpine", "sha256:a10e64dd139b7387337c7fbe8aca31b959b57b2fd4c8ae20a02cf1d6ea424dce", "alpine", "", "musl"},
	// Alpine, despite the tag. See imageBase.
	"composer:2": {"composer:2", "sha256:4d71c3c2109c61d5415544264b59ad4087e4c5b7244481723664138fd36d5040", "alpine", "", "musl"},
	// Debian, not alpine, and the difference is the whole ecosystem: a gem
	// with a C extension compiles at install time, every time, and alpine
	// ships no compiler. See imageForRuntimeVersion.
	"ruby:3":               {"ruby:3", "sha256:364bd08657bc1106373e8c2fc1b39b68f384f339decc5867374caf6e2e112927", "debian", "", "glibc"},
	"dart:3.13.0":          {"dart:3.13.0", "sha256:8b6175f6c6b89aaf31ffdace4a22d17715c07f1cf3a772dadb10c658f779e23d", "debian", "", "glibc"},
	"elixir:1.20.1-alpine": {"elixir:1.20.1-alpine", "sha256:f50894ff69b0d07b310fe9c97b48b3475568ecccb7f0ccd7c350a789feb395a3", "alpine", "", "musl"},

	// The measured browser verifier: Node 22.14.0 and Chrome for Testing
	// 134.0.6998.35.
	"ghcr.io/puppeteer/puppeteer:24.4.0": {"ghcr.io/puppeteer/puppeteer:24.4.0", "sha256:ca2087099ad5769b74c89135c663cbb2a76e07d3e261bb3e2da83be98409a68a", "debian", "", "glibc"},

	// Java. The legacy Maven lane and its Gradle counterpart keep Alpine and
	// Temurin; every exact-JDK line uses Amazon Corretto on AL2023 so Java is
	// the only moving dimension across the matrix.
	"maven:3.9.11-eclipse-temurin-21-alpine": {"maven:3.9.11-eclipse-temurin-21-alpine", "sha256:922927df2c662cdd47ddb116443d6bec4696cfae3de1a0ddac8fcc7b87ce61ae", "alpine", "", "musl"},
	"gradle:8.14.3-jdk21-alpine":             {"gradle:8.14.3-jdk21-alpine", "sha256:d20561a56ff27350ea778b8151f6af913c76e9d35b6a135f927ee16e3ce8193c", "alpine", "", "musl"},
	"maven:3.9.11-amazoncorretto-8-al2023":   {"maven:3.9.11-amazoncorretto-8-al2023", "sha256:80f411ee8dc37def5bb6808f3b2698d5fac5a16b6797ffc8fc1fbce2df71b49e", "2023", "amzn", "glibc"},
	"maven:3.9.11-amazoncorretto-11-al2023":  {"maven:3.9.11-amazoncorretto-11-al2023", "sha256:07b7514c1fce56f9dece12a9daea49312fff36f4b3449e8c314667587f997be0", "2023", "amzn", "glibc"},
	"maven:3.9.11-amazoncorretto-17-al2023":  {"maven:3.9.11-amazoncorretto-17-al2023", "sha256:6374befde1891b069f5297714525d2a6c03cd0f410070fb53b2842b4d8118c63", "2023", "amzn", "glibc"},
	"maven:3.9.11-amazoncorretto-21-al2023":  {"maven:3.9.11-amazoncorretto-21-al2023", "sha256:b0e00d2581674e0c12392bb88075a2835e73af86af48bbdb8eeec3d2e993ea40", "2023", "amzn", "glibc"},
	"maven:3.9.11-amazoncorretto-25-al2023":  {"maven:3.9.11-amazoncorretto-25-al2023", "sha256:3d55eb28eae103300391509a5ac8cfc918d4a35dbfd087ef5472023949682791", "2023", "amzn", "glibc"},
	"gradle:8.14.3-jdk8-corretto-al2023":     {"gradle:8.14.3-jdk8-corretto-al2023", "sha256:ec6379bf6453a09b608f7feb4e52a53f28edc2a1acc8a07aff6328b906bf20d6", "2023", "amzn", "glibc"},
	"gradle:8.14.3-jdk11-corretto-al2023":    {"gradle:8.14.3-jdk11-corretto-al2023", "sha256:93d9f84a044faf9b345b7c126772ba719195318b0e848e322c6f1a977232e012", "2023", "amzn", "glibc"},
	"gradle:8.14.3-jdk17-corretto-al2023":    {"gradle:8.14.3-jdk17-corretto-al2023", "sha256:9a40f91169b9685e9d25c73722b7f8dea9e1e7aa43dc4fc8a1acda1614a05eca", "2023", "amzn", "glibc"},
	"gradle:8.14.3-jdk21-corretto-al2023":    {"gradle:8.14.3-jdk21-corretto-al2023", "sha256:05cb2f8b4a77587b3de1cd7ae003204eb8c1dc9db48a52c9cebd29d0041c949b", "2023", "amzn", "glibc"},
	"gradle:9.7.0-jdk25-corretto-al2023":     {"gradle:9.7.0-jdk25-corretto-al2023", "sha256:bb35f016497202d8342ff68fe5d45b74a94a3d9043a3d2cced0061818abebeef", "2023", "amzn", "glibc"},

	// Windows verifier lanes. There is no libc to report on Windows; the
	// base is the server core release the image family names.
	"golang:1.26-windowsservercore-ltsc2022": {"golang:1.26-windowsservercore-ltsc2022", "sha256:2a3365ef5cb38e4ebaafe073a68a97989322fda2a582ee067ef9fd59a0551692", "windowsservercore", "", ""},
	"python:3.12-windowsservercore-ltsc2022": {"python:3.12-windowsservercore-ltsc2022", "sha256:035418c04b5e8fcb13c6b23f6c801a52c510c43e8bf27e2379d26ad8c40c87a7", "windowsservercore", "", ""},
	"python:3.14-windowsservercore-ltsc2022": {"python:3.14-windowsservercore-ltsc2022", "sha256:f7af89224fd778159f655fceca89562fb978e12a6c66e191fd2c27800ec994e5", "windowsservercore", "", ""},
}

// byRef indexes the registry by the reference that is actually executed, so
// a stage image can be traced back to the entry that describes it.
var byRef = func() map[string]verifierImage {
	m := make(map[string]verifierImage, len(verifierImages))
	for _, img := range verifierImages {
		m[img.ref()] = img
	}
	return m
}()

// pinned resolves an alias to the immutable reference a stage may run.
//
// It panics on an unknown alias, at package initialization, because every
// call site is a compile-time constant in this package: a selector naming an
// image the registry does not carry is a programming error that must not
// survive to a worker, where it would become an unpinned run.
func pinned(alias string) string {
	img, ok := verifierImages[alias]
	if !ok {
		panic(fmt.Sprintf("sandbox: %q is not in the verifier image registry", alias))
	}
	return img.ref()
}

func registryEntryFor(ref string) (verifierImage, bool) {
	img, ok := byRef[ref]
	return img, ok
}

// PublishedImage reports whether a receipt's image reference is an entry this
// registry publishes, and returns it.
//
// Shape and provenance are different claims, and only the shape was askable
// from outside this package. `node:22-alpine@sha256:aaaa…` is a perfectly
// well-formed immutable pin naming bytes that were never published, and the
// receipt endpoint accepts it: rejecting an unrecognised digest would break
// every worker running a newer registry than the server, so the server checks
// the form and nothing checks the provenance.
//
// R2C-81 then had to answer "did production execute the PUBLISHED digest"
// three times (2026-08-23, 08-24, 08-29), and each audit had to be planted
// inside this package to reach byRef — or retype the digests, which is the
// drift this file exists to stop. One exported lookup makes the answer
// recomputable from where the receipts actually live.
func PublishedImage(reference string) (domain.VerifierImage, bool) {
	img, ok := registryEntryFor(reference)
	if !ok {
		return domain.VerifierImage{}, false
	}
	return domain.VerifierImage{Reference: img.ref(), Digest: img.digest}, true
}

// PublishedReferences lists every immutable reference the registry publishes,
// sorted, so a caller can enumerate the lanes without a copy of the table.
func PublishedReferences() []string {
	refs := make([]string, 0, len(byRef))
	for ref := range byRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

// imageBase reports the distribution bucket and libc of a verifier image.
//
// A receipt states which libc the contract ran against, and musl versus
// glibc is the dimension the grader treats as decisive for whether a package
// with a native module loads at all — so this cannot be a guess. It was
// inferred from the image NAME once: composer:2 is an Alpine image that does
// not say so in its tag, so every PHP receipt claimed glibc for a run on
// musl, and a caller on Debian was told a musl-verified sample matched their
// libc exactly.
//
// The name fallback remains only for an image outside the registry, which no
// lane can now select; it over-claims glibc rather than musl, because musl is
// the narrower claim and therefore the more misleading one to invent.
func imageBase(image string) (bucket, libc string) {
	if img, ok := registryEntryFor(image); ok {
		return img.bucket, img.libc
	}
	if strings.Contains(image, "alpine") {
		return "alpine", "musl"
	}
	return "debian", "glibc"
}

// verifierImageOf reports the image identity a receipt records for a stage
// reference: the canonical immutable reference and, separately, the content
// digest that decides which bytes ran.
//
// The digest is the registry index digest and NOT the daemon's local image
// ID. The local ID is an implementation detail of the image store — a
// containerd-backed daemon reports the index digest there while a classic
// graph driver reports the config digest — so signing it would make two
// workers running identical bytes disagree, which is precisely the property
// this field exists to establish.
func verifierImageOf(ref string) *domain.VerifierImage {
	img, ok := registryEntryFor(ref)
	if !ok {
		return nil
	}
	return &domain.VerifierImage{Reference: img.ref(), Digest: img.digest}
}

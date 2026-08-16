package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// runSeederCreate mints a seeder identity and its api token.
//
// An operator command on the host, for the same reason quarantine is one:
// whoever can reach the database can already do anything, and requiring
// shell access is the honest boundary. Inventing an authenticated admin API
// would add a write surface to defend for an action performed a handful of
// times.
//
// It exists because publishing at scale needs an identity. Anonymous
// publishing is capped at ten an hour — deliberately, because anyone at all
// may do it — and seeding a network from nothing means thousands of
// samples, which is weeks of waiting on a limiter. An identified seeder is
// a different situation: the account is revocable and every sample it
// uploads is attributed to it, which is the accountability the anonymous
// cap substitutes for. Until now the only way to become one was GitHub
// OAuth, which the operator seeding their own network cannot use to
// authorize a script.
//
//	docker compose exec -T server csx-server seeder-create <login>
//
// The token is shown ONCE and only its hash is stored, so a leaked copy can
// be revoked by minting a new one rather than by trusting that nobody read
// the database.
func runSeederCreate(cfg serverstore.ServerConfig, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seeder-create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: csx-server seeder-create <login>")
		return 2
	}
	login := strings.TrimSpace(fs.Arg(0))
	if login == "" {
		fmt.Fprintln(stderr, "csx-server seeder-create: login must not be empty")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, ok := openMigrated(ctx, cfg, stderr)
	if !ok {
		return 1
	}
	defer pg.Close()

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		fmt.Fprintf(stderr, "csx-server seeder-create: %v\n", err)
		return 1
	}
	token := "csx_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))

	// githubID 0: this identity did not come from GitHub, and pretending it
	// did would put a number in the row that means someone else's account.
	if err := pg.SaveIdentity(ctx, login, 0, "", hex.EncodeToString(sum[:])); err != nil {
		fmt.Fprintf(stderr, "csx-server seeder-create: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Seeder %q created.\n\n", login)
	fmt.Fprintf(stdout, "  %s\n\n", token)
	fmt.Fprintln(stdout, "This is the only time the token is shown; the server keeps a hash of it.")
	fmt.Fprintln(stdout, "Use it from the publishing machine:")
	fmt.Fprintf(stdout, "  csx config set apiToken %s\n", token)
	fmt.Fprintf(stdout, "  csx sample publish <sampleId> --seeder %s\n", login)
	return 0
}

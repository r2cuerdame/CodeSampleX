package serverstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// PostgreSQL's default libc collation can lowercase only ASCII (including
// en_US.utf8 on Alpine). The indexed key must use PG17's builtin Unicode table
// and match Go across every case-changing rune, plus encoded identity edges.
func TestIntegrationBuilderCoordinatesMatchGoUnicodeAndEscaping(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	var raw, want []string
	for _, purl := range []string{
		"pkg:NPM/Axios@1.2.3", "pkg:npm/@Scope/Name@1.0.0",
		"pkg:npm/%40Scope/Name@1.0.0", "pkg:npm/%2540foo@1.0.0",
		"pkg:npm/%2561lias@2.0.0", "pkg:npm/scope%2fname@1.0.0",
		"pkg:npm/a@b@1.0.0", "pkg:npm/name%25literal@1.0.0",
		"pkg:golang/github.com/Owner/Module/v2@2.0.0",
		"pkg:maven/org.example:Library@1.0.0", "pkg:NPİM/name@1.0.0",
	} {
		p, err := domain.ParsePURL(purl)
		if err != nil {
			t.Fatal(err)
		}
		raw, want = append(raw, purl), append(want, builderCoord(p))
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		s := string(r)
		if strings.ToLower(s) == s {
			continue
		}
		purl := "pkg:npm/pre" + s + "post@1.0.0"
		p, err := domain.ParsePURL(purl)
		if err != nil {
			t.Fatal(err)
		}
		raw, want = append(raw, purl), append(want, builderCoord(p))
	}
	builderSQL(t, pg, func(c *pgx.Conn) error {
		var locale string
		if err := c.QueryRow(ctx, "SELECT datctype FROM pg_database WHERE datname=current_database()").Scan(&locale); err != nil {
			return err
		}
		t.Logf("Go Unicode=%s PG lc_ctype=%s case-changing inputs=%d", unicode.Version, locale, len(raw))
		rows, err := c.Query(ctx, "SELECT p,w,builder_purl_coord(p) FROM unnest($1::text[],$2::text[]) AS x(p,w) WHERE builder_purl_coord(p) IS DISTINCT FROM w", raw, want)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p, w, got string
			if err := rows.Scan(&p, &w, &got); err != nil {
				return err
			}
			t.Errorf("%s", fmt.Sprintf("coord(%q): SQL=%q Go=%q", p, got, w))
		}
		return rows.Err()
	})
}

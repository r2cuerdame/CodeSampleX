package serverstore

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationBuilderCorrectionFailsClosedOnUnrecoverableHistory(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	builderFixtureSample(t, pg, "unrecoverable", []string{"pkg:npm/original@1.0.0"}, []string{"original"}, "")
	builderSQL(t, pg, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `UPDATE samples SET manifest='{"packages":[17],"subject":"pkg:npm/intermediate@2.0.0"}' WHERE sample_id='unrecoverable'`)
		return err
	})
	before, _, err := pg.GetSample(ctx, "unrecoverable")
	if err != nil {
		t.Fatal(err)
	}
	corrected := before
	corrected.ManifestJSON = `{"packages":["pkg:npm/corrected@3.0.0"]}`
	if err := pg.SaveSample(ctx, corrected); err == nil || !strings.Contains(err.Error(), "refusing to overwrite untrusted builder sample unrecoverable") {
		t.Fatalf("correction should fail closed on unrecoverable prior attribution: %v", err)
	}
	after, _, err := pg.GetSample(ctx, "unrecoverable")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed correction changed retained source: before=%+v after=%+v err=%v", before, after, err)
	}
	builderSQL(t, pg, func(c *pgx.Conn) error {
		var stale bool
		if err := c.QueryRow(ctx, builderProjectionReadinessSQL).Scan(&stale); err != nil {
			return err
		}
		if !stale {
			t.Fatal("failed correction cleared the fail-closed projection guard")
		}
		return nil
	})
}

func TestSampleBuilderProjectionPreservesLiteralPercentIdentity(t *testing.T) {
	raw := `{"packages":["pkg:npm/%2540foo@1.0.0"],"subject":"pkg:npm/%2561lias@2.0.0"}`
	want := []string{"pkg:npm/%2540foo@1.0.0", "pkg:npm/%2561lias@2.0.0"}
	got, err := deriveSampleBuilderProjection(raw)
	if err != nil || !reflect.DeepEqual(got.purls, want) {
		t.Fatalf("projection rewrote reparsed literal-percent identity: got=%+v err=%v", got, err)
	}
}

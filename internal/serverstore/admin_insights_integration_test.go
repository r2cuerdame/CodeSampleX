package serverstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationAdminInsightsUseReceiptFactsNotManifestClaims(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	samples := []SampleRow{
		{
			SampleID: "sha256:admin-insight-v2", SizeBytes: 1,
			ManifestJSON: `{"schemaVersion":1,"packages":["pkg:pypi/requests@1.0.0"],` +
				`"environment":{"ecosystem":"pypi"}}`,
		},
		{
			SampleID: "sha256:admin-insight-v1", SizeBytes: 1,
			ManifestJSON: `{"schemaVersion":1,"packages":["pkg:npm/lodash@4.17.0"],` +
				`"environment":{"ecosystem":"npm"}}`,
		},
	}
	for _, sample := range samples {
		if err := pg.SaveSample(ctx, sample); err != nil {
			t.Fatalf("SaveSample(%s): %v", sample.SampleID, err)
		}
	}

	receipts := []ReceiptRow{
		{
			ReceiptID: "receipt-admin-v2", SampleID: samples[0].SampleID, PeerID: "peer-a", EnvHash: "env-a", ContractResult: "PASS",
			// Deliberately differs from the manifest. Matrix insight must report
			// the actual receipt environment and resolved version/package.
			ReceiptJSON: `{"schemaVersion":2,"environment":{"ecosystem":"npm"},` +
				`"stages":{"resolve":"PASS","contract":"PASS"},` +
				`"resolvedPackages":["pkg:npm/axios@2.0.0"]}`,
		},
		{
			ReceiptID: "receipt-admin-v1", SampleID: samples[1].SampleID, PeerID: "peer-b", EnvHash: "env-b", ContractResult: "PASS",
			ReceiptJSON: `{"schemaVersion":1,"environment":{"ecosystem":"cargo"},` +
				`"stages":{"contract":"PASS"}}`,
		},
		{
			ReceiptID: "receipt-admin-maven", SampleID: samples[1].SampleID, PeerID: "peer-java", EnvHash: "env-java", ContractResult: "PASS",
			// Maven is a first-class Java/JVM ecosystem in both the receipt mix
			// and resolved-package depth. It must not be folded into "other".
			ReceiptJSON: `{"schemaVersion":2,"environment":{"ecosystem":"maven"},` +
				`"stages":{"resolve":"PASS","contract":"PASS"},` +
				`"resolvedPackages":["pkg:maven/com.fasterxml.jackson.core/jackson-databind@2.21.4"]}`,
		},
		{
			ReceiptID: "receipt-admin-fail", SampleID: samples[1].SampleID, PeerID: "peer-c", EnvHash: "env-c", ContractResult: "FAIL",
			ReceiptJSON: `{"schemaVersion":2,"environment":{"ecosystem":"pypi"},` +
				`"stages":{"resolve":"PASS","contract":"FAIL"},` +
				`"resolvedPackages":["pkg:pypi/requests@1.0.0"]}`,
		},
		{
			ReceiptID: "receipt-admin-mismatched-pass", SampleID: samples[1].SampleID, PeerID: "peer-d", EnvHash: "env-d", ContractResult: "PASS",
			// A denormalized PASS must not promote JSON that says the signed
			// contract failed into either the ecosystem or package-depth views.
			ReceiptJSON: `{"schemaVersion":2,"environment":{"ecosystem":"hex"},` +
				`"stages":{"resolve":"PASS","contract":"FAIL"},` +
				`"resolvedPackages":["pkg:hex/poison@1.0.0"]}`,
		},
		{
			ReceiptID: "receipt-admin-fixed-other", SampleID: samples[1].SampleID, PeerID: "peer-e", EnvHash: "env-e", ContractResult: "PASS",
			// Direct-store corruption cannot become an arbitrary dashboard
			// label; every unsupported value is folded into one fixed bucket.
			ReceiptJSON: `{"schemaVersion":1,"environment":{"ecosystem":"future-ecosystem"},` +
				`"stages":{"contract":"PASS"}}`,
		},
		{
			ReceiptID: "receipt-admin-custom-result", SampleID: samples[1].SampleID, PeerID: "peer-f", EnvHash: "env-f", ContractResult: "CUSTOM-RESULT",
			ReceiptJSON: `{"schemaVersion":1,"environment":{"ecosystem":"npm"},` +
				`"stages":{"contract":"SKIPPED"}}`,
		},
	}
	for _, receipt := range receipts {
		if err := pg.SaveReceipt(ctx, receipt); err != nil {
			t.Fatalf("SaveReceipt(%s): %v", receipt.ReceiptID, err)
		}
	}

	now := time.Now().UTC().Add(time.Minute)
	for i, samples := range []int64{8, 9, 11} {
		day := now.AddDate(0, 0, i-2).Format("2006-01-02")
		doc := fmt.Sprintf(`{"evidence":%d,"verifiedSamples":%d,"packages":%d}`, 100+i, samples, 5+i)
		if err := pg.SetStatsDaily(ctx, day, doc); err != nil {
			t.Fatalf("SetStatsDaily(%s): %v", day, err)
		}
	}
	// The stats window is defined in UTC calendar dates. A non-UTC database
	// session must not shift midnight time.Time parameters across a date
	// boundary before PostgreSQL's ::date cast.
	if err := pg.withConn(ctx, func(conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET TIME ZONE 'Pacific/Honolulu'")
		return err
	}); err != nil {
		t.Fatalf("set non-UTC session timezone: %v", err)
	}

	got, err := pg.AdminInsights(ctx, now)
	if err != nil {
		t.Fatalf("AdminInsights: %v", err)
	}
	if got.Verification.Pass != 5 || got.Verification.Fail != 1 || got.Verification.Unclassified != 1 || got.Verification.Total() != 7 {
		t.Fatalf("verification = %+v, want PASS=5 FAIL=1 unclassified=1 total=7", got.Verification)
	}
	ecosystems := map[string]int64{}
	for _, row := range got.Ecosystems {
		ecosystems[row.Ecosystem] = row.Verifications
	}
	if ecosystems["npm"] != 1 || ecosystems["cargo"] != 1 || ecosystems["maven"] != 1 || ecosystems["other"] != 1 || ecosystems["pypi"] != 0 || ecosystems["hex"] != 0 {
		t.Fatalf("receipt ecosystems = %+v, want npm=1 cargo=1 maven=1 other=1 and no failed/mismatched pypi or hex", ecosystems)
	}
	depth := map[string]int64{}
	for _, row := range got.PackageDepth {
		depth[row.Ecosystem+"/"+row.Name] = row.VerifiedSamples
	}
	if len(depth) != 2 || depth["npm/axios"] != 1 || depth["maven/com.fasterxml.jackson.core/jackson-databind"] != 1 {
		t.Fatalf("resolved package depth = %+v, want npm/axios and Maven jackson-databind", got.PackageDepth)
	}
	if len(got.Daily) == 0 || len(got.Daily) > 31 {
		t.Fatalf("daily rows = %d, want 1..31", len(got.Daily))
	}
	todayFound := false
	for _, row := range got.Daily {
		if row.Day.Format("2006-01-02") == now.Format("2006-01-02") {
			todayFound = true
			break
		}
	}
	if !todayFound {
		t.Fatalf("daily rows omitted UTC today %s under non-UTC database session: %+v", now.Format("2006-01-02"), got.Daily)
	}
}

//go:build calibration

// longctx-calibrate runs the bounded long context calibration approved for #139: count-only
// calls for the approved account and two models, 98 attempts in total across all runs.
//
// It is built only with the calibration tag and is meant to run on the gateway host, so the
// account's credential and proxy never leave it. The database connection is read-only
// (default_transaction_read_only=on, verified) and no migration is run.
//
//	go build -tags=calibration -o longctx-calibrate ./cmd/longctx-calibrate
//	./longctx-calibrate            # dry run: prepare and verify every request, send nothing
//	./longctx-calibrate -live      # the authorized experiment
//
// The ledger and report live in a fixed directory, $HOME/longctx-calibration-139, so a new
// output path cannot reset the budget.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

const approvedAccountID = 71

// originalManifest is the plan the host ledger is bound to (two dry runs, before any call).
const originalManifest = "2d5bb244ed3b4d2ef7fc289fa08bcd57938f3b43e8888bc9116ebdcfd2c9adf1"

// missAsNil reports a missing fingerprint key as (nil, nil), the miss the service expects;
// every other error stays a read failure.
type missAsNil struct{ service.IdentityCache }

func (c missAsNil) GetFingerprint(ctx context.Context, accountID int64) (*service.Fingerprint, error) {
	fp, err := c.IdentityCache.GetFingerprint(ctx, accountID)
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return fp, err
}

func main() {
	live := flag.Bool("live", false, "send the count calls (default: dry run)")
	diagnose := flag.Bool("diagnose-readiness", false, "one separately authorized diagnostic after the first readiness 400")
	resumeCheck := flag.Bool("resume-check", false, "continuation A: verify bodies, count copies and ledger history; send nothing, write nothing")
	resume := flag.Bool("resume", false, "continuation A: append the one-time resume entry (if not yet) and run the remaining 96 jobs")
	flag.Parse()
	modes := 0
	for _, on := range []bool{*live, *diagnose, *resumeCheck, *resume} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		log.Fatal("choose at most one of -live, -diagnose-readiness, -resume-check, -resume")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("home dir: %v", err)
	}
	dir := filepath.Join(home, "longctx-calibration-139")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatalf("ledger dir: %v", err)
	}

	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := timezone.Init(cfg.Timezone); err != nil {
		log.Fatalf("timezone: %v", err)
	}
	drv, err := entsql.Open(dialect.Postgres, cfg.Database.DSNWithTimezone(cfg.Timezone)+" default_transaction_read_only=on")
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	db := drv.DB()
	db.SetMaxOpenConns(2)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	var readOnly string
	if err := db.QueryRowContext(ctx, "SHOW default_transaction_read_only").Scan(&readOnly); err != nil || readOnly != "on" {
		log.Fatalf("database session is not read-only (%q, %v); refusing to continue", readOnly, err)
	}
	client := dbent.NewClient(dbent.Driver(drv))

	account, err := repository.NewAccountRepository(client, db, nil).GetByID(ctx, approvedAccountID)
	if err != nil {
		log.Fatalf("load account: %v", err)
	}
	if err := service.CheckLongContextCalibrationAccount(account, approvedAccountID, time.Now()); err != nil {
		log.Fatalf("account check: %v", err)
	}
	settings := service.NewSettingService(repository.NewSettingRepository(client), cfg)
	tls := service.NewTLSFingerprintProfileService(repository.NewTLSFingerprintProfileRepository(client), nil)
	// Reuse or generate identity once in the local calibration directory, never in Redis.
	rdb := repository.InitRedis(cfg)
	defer func() { _ = rdb.Close() }()
	idCache, err := service.NewCalibrationIdentitySnapshot(ctx, missAsNil{repository.NewIdentityCache(rdb)}, account, dir)
	if err != nil {
		log.Fatalf("identity preflight: %v", err)
	}
	k := service.NewLongContextCalibrator(cfg, repository.NewHTTPUpstream(cfg), settings, tls, idCache)
	if *diagnose {
		if err := service.RunLongContextReadinessDiagnostic(ctx, k, account, dir); err != nil {
			log.Fatalf("diagnostic: %v", err)
		}
		log.Print("diagnostic completed; calibration remains stopped")
		return
	}

	var report *service.LongContextCalibrationReport
	if *resumeCheck || *resume {
		report, err = service.RunLongContextCalibrationResume(ctx, k, account, dir, originalManifest, buildRevision(), *resume, log.Printf)
	} else {
		report, err = service.RunLongContextCalibration(ctx, k, account, dir, *live, log.Printf)
	}
	if report != nil {
		log.Printf("manifest_sha256=%s report_dir=%s", report.Manifest, dir)
	}
	if err != nil {
		log.Fatalf("calibration: %v", err)
	}
}

// buildRevision is the source commit embedded by the Go toolchain, marked when the tree was dirty.
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	rev, dirty := "", false
	for _, kv := range info.Settings {
		switch kv.Key {
		case "vcs.revision":
			rev = kv.Value
		case "vcs.modified":
			dirty = kv.Value == "true"
		}
	}
	if rev != "" && dirty {
		rev += "+dirty"
	}
	return rev
}

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
	"flag"
	"log"
	"os"
	"path/filepath"
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
)

const approvedAccountID = 71

func main() {
	live := flag.Bool("live", false, "send the count calls (default: dry run)")
	diagnose := flag.Bool("diagnose-readiness", false, "one separately authorized diagnostic after the first readiness 400")
	flag.Parse()
	if *live && *diagnose {
		log.Fatal("choose either live calibration or readiness diagnostic")
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
	idCache, err := service.NewCalibrationIdentitySnapshot(ctx, repository.NewIdentityCache(rdb), account, dir)
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

	report, err := service.RunLongContextCalibration(ctx, k, account, dir, *live, log.Printf)
	if report != nil {
		log.Printf("manifest_sha256=%s report_dir=%s", report.Manifest, dir)
	}
	if err != nil {
		log.Fatalf("calibration: %v", err)
	}
}

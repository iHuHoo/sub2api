.PHONY: build generate test test-unit test-integration test-calibration test-e2e

VERSION ?= $(shell ./scripts/resolve-version.sh)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server

test:
	go test ./...
	golangci-lint run ./...

test-unit:
	go test -race -tags=unit ./...

test-integration:
	go test -tags=integration ./...

# Full-corpus calibration tests (#139): offline, no provider calls; kept out of test-unit for its timeout.
test-calibration:
	go test -race -timeout=8m -tags='unit calibration' -run '^(TestRunLongContextCalibrationDry|TestCalibrationIdentityIsReadOnly|TestRunLongContextCalibrationResumeCheck|TestCalibrationIdentitySnapshotReuse)$$' ./internal/service
	go test -race -tags=calibration ./cmd/longctx-calibrate

test-e2e:
	./scripts/e2e-test.sh

test-e2e-local:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...

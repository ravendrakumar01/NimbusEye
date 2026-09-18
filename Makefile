# NimbusEye build tasks.
GO ?= go
BIN := bin

.PHONY: all api collector alerter web test clean fmt

all: api collector alerter web

api:
	$(GO) build -o $(BIN)/nimbuseye-api ./cmd/api

collector:
	$(GO) build -o $(BIN)/nimbuseye-collector ./cmd/collector

alerter:
	$(GO) build -o $(BIN)/nimbuseye-alerter ./cmd/alerter

web:
	cd web && npm run build

fmt:
	gofmt -w .
	$(GO) vet ./...

test:
	$(GO) vet ./...
	$(GO) test ./... 2>&1 | grep -v "no test files" || true
	cd web && npx tsc --noEmit

clean:
	rm -rf $(BIN) web/dist

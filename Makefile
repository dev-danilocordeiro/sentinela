# Developer shortcuts. The binary itself is built with `go build ./cmd/sentinela`.
.PHONY: build test lint eval eval-replay

build:
	go build -o sentinela ./cmd/sentinela

# Integration tests of the store run only with SENTINELA_TEST_DATABASE_URL (a throwaway database).
test:
	go test ./...

lint:
	golangci-lint run ./...

# Live Judge eval: spends model calls (provider from evals/config.yaml, or EVAL_FLAGS=--provider
# anthropic_api), writes evals/results/latest.{json,md} and the recordings the replay uses.
eval:
	go run ./cmd/sentinela eval $(EVAL_FLAGS)

# Replays the recorded live run without calling a model and fails if the prompt, the dataset or
# the scoring no longer reproduce evals/results/latest.json. Runs in CI.
eval-replay:
	@if [ -f evals/results/latest.json ]; then \
		go run ./cmd/sentinela eval --provider replay --check evals/results/latest.json; \
	else \
		echo "eval-replay: no recorded live run (evals/results/latest.json); run 'make eval' first. Skipped."; \
	fi

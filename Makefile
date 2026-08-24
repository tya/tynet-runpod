.PHONY: help build deploy run destroy gpus resize status logs test cover test-integration clean

-include .env.local
export

.DEFAULT_GOAL := help

help: ## List available targets
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "%-10s %s\n", $$1, $$2}'

build: ## Build the tynet-runpod binary
	go build -o tynet-runpod .

deploy: ## Create the GPU pod, wait for it to boot
	go run . deploy

run: ## Exec claude against the running pod (ARGS="..." to pass flags)
	go run . run $(ARGS)

destroy: ## Terminate the pod (keeps the cache volume)
	go run . destroy

gpus: ## List GPU stock in network-volume-capable data centers
	go run . gpus

resize: ## Re-apply vllm args to a running pod and restart it
	go run . resize

status: ## Show the pod's status and live resource utilization
	go run . status

logs: ## Stream the pod's logs (ARGS="-tail 500 -source container")
	go run . logs $(ARGS)

test: ## Run the test suite
	go test ./...

cover: ## Run tests and open an HTML coverage report in the browser
	go test -coverprofile=/tmp/tynet-runpod-cover.out ./...
	go tool cover -html=/tmp/tynet-runpod-cover.out

test-integration: ## Deploy a REAL pod, verify it replies, then destroy it (costs GPU billing + several minutes)
	go test -tags integration -run TestIntegration -v -timeout 20m ./...

clean: ## Remove build/test artifacts (binary, coverage profile)
	rm -f tynet-runpod /tmp/tynet-runpod-cover.out

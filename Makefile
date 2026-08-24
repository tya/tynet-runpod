.PHONY: help deploy run destroy gpus resize

-include .env.local
export

.DEFAULT_GOAL := help

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "%-10s %s\n", $$1, $$2}'

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

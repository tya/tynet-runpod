.PHONY: deploy run destroy

deploy:
	scripts/deploy-pod.sh

run:
	scripts/claude-runpod.sh

destroy:
	scripts/destroy-pod.sh

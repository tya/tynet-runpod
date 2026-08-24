.PHONY: deploy run destroy gpus resize

deploy:
	go run . deploy

run:
	go run . run $(ARGS)

destroy:
	go run . destroy

gpus:
	go run . gpus

resize:
	go run . resize

.PHONY: up build agent-test web-dev

## up: собрать и запустить контейнер (docker compose)
up:
	docker compose up -d --build

## build: собрать образ без запуска
build:
	docker compose build

## agent-test: юнит-тесты агента внутри golang-контейнера (Go на хосте не нужен)
agent-test:
	docker run --rm -v $(CURDIR)/image/agent:/src -w /src golang:1.25 go test ./...

## web-dev: dev-сервер фронтенда внутри node-контейнера (Node на хосте не нужен)
web-dev:
	docker run --rm -it -v $(CURDIR)/web:/src -w /src -p 5173:5173 node:22 \
		sh -c "npm install && npm run dev -- --host 0.0.0.0"

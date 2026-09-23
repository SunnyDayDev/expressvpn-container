.PHONY: up build agent-test web-dev e2e e2e-proxy-name

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

## e2e: сквозной тест переключения uplink'а на локальном Docker (SCENARIO=s1,s3 — выборочно).
## Нужны собранный образ (make build) и том с выполненным входом в ExpressVPN:
## по умолчанию берётся проект compose основного чекаута (E2E_PROJECT).
E2E_PROJECT ?= expressvpn-container
E2E_COMPOSE = docker compose -p $(E2E_PROJECT) -f docker-compose.yml -f test/e2e/compose.e2e.yml
e2e:
	$(E2E_COMPOSE) up -d --no-build
	E2E_COMPOSE="$(E2E_COMPOSE)" test/e2e/uplink-switch.sh $(or $(SCENARIO),all); \
		status=$$?; $(E2E_COMPOSE) rm -sf socks-a socks-b >/dev/null 2>&1; exit $$status

## e2e-proxy-name: смена socks5-прокси «IP → имя compose-сервиса» на отдельном стенде
## из текущего кода; вход в ExpressVPN не нужен, рабочий Detour не затрагивается.
e2e-proxy-name:
	test/e2e/proxy-name.sh

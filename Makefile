COMPOSE_PROJECT ?= jetmon-bridge

COMPOSE       = docker compose -p $(COMPOSE_PROJECT) -f docker/docker-compose.yml --env-file docker/.env
COMPOSE_LOCAL = $(COMPOSE) -f docker/docker-compose.local.yml

.PHONY: up up-local down down-local down-clean build _require-dsn

## up: start the bridge against Jetmon 1's read replica (requires JETMON_DSN in docker/.env)
up: _require-dsn
	$(COMPOSE) up --build

_require-dsn:
	@if [ ! -f docker/.env ] || ! grep -qsP '^JETMON_DSN=.+' docker/.env; then \
	  echo ""; \
	  echo "  error: JETMON_DSN is not set (or is empty) in docker/.env"; \
	  echo "  For a real database:  set JETMON_DSN in docker/.env, then run: make up"; \
	  echo "  For local test data:  run: make up-local"; \
	  echo ""; \
	  exit 1; \
	fi

## up-local: start the bridge with a local seeded MySQL container
up-local:
	$(COMPOSE_LOCAL) up --build

## down: stop containers started with 'make up'
down:
	$(COMPOSE) down

## down-local: stop containers started with 'make up-local'
down-local:
	$(COMPOSE_LOCAL) down

## down-clean: stop local containers and remove the MySQL data volume
down-clean:
	$(COMPOSE_LOCAL) down -v

## build: compile the binary for the current platform
build:
	go build -o bin/jetmon-bridge .

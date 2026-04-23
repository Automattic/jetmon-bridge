COMPOSE      = docker compose -f docker/docker-compose.yml --env-file docker/.env
COMPOSE_LOCAL = $(COMPOSE) -f docker/docker-compose.local.yml

.PHONY: up up-local down down-clean build _require-dsn

## up: start the bridge against Jetmon 1's read replica (requires JETMON_DSN in docker/.env)
up: _require-dsn
	$(COMPOSE) up --build

_require-dsn:
	@if [ ! -f docker/.env ] || ! grep -qs '^JETMON_DSN=' docker/.env; then \
	  echo ""; \
	  echo "  error: JETMON_DSN is not set in docker/.env"; \
	  echo "  For a real database:  set JETMON_DSN in docker/.env, then run: make up"; \
	  echo "  For local test data:  run: make up-local"; \
	  echo ""; \
	  exit 1; \
	fi

## up-local: start the bridge with a local seeded MySQL container
up-local:
	$(COMPOSE_LOCAL) up --build

## down: stop containers
down:
	$(COMPOSE) down

## down-clean: stop containers and remove the local MySQL data volume
down-clean:
	$(COMPOSE_LOCAL) down -v

## build: compile the binary for the current platform
build:
	go build -o bin/jetmon-bridge .

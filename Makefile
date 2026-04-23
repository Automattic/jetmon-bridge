COMPOSE      = docker compose -f docker/docker-compose.yml --env-file docker/.env
COMPOSE_LOCAL = $(COMPOSE) -f docker/docker-compose.local.yml

.PHONY: up up-local down down-clean build

## up: start the bridge against Jetmon 1's read replica (requires JETMON_DSN in docker/.env)
up:
	$(COMPOSE) up --build

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

COMPOSE_PROJECT ?= jetmon-bridge
JETMON_V1_DSN ?= root:123456@tcp(jetmon-v1-mysql:3306)/jetmon_db
JETMON_V1_WRITE ?= false
JETMON_V1_WRITE_DSN ?= $(JETMON_V1_DSN)
JETMON_V1_BUCKET ?= 0
JETMON_V1_HISTORY_PATH ?= /var/lib/jetmon-bridge/history.db
JETMON_V1_HISTORY_POLL_INTERVAL ?= 15s
JETMON_V1_HISTORY_BOOTSTRAP ?= true

COMPOSE       = docker compose -p $(COMPOSE_PROJECT) -f docker/docker-compose.yml --env-file docker/.env
COMPOSE_LOCAL = $(COMPOSE) -f docker/docker-compose.local.yml

.PHONY: up up-local up-jetmon-v1 down down-local down-clean build test vet check _ensure-env _ensure-shared-network _require-dsn

## up: start the bridge against Jetmon 1's read replica (requires JETMON_DSN in docker/.env)
up: _require-dsn _ensure-shared-network
	$(COMPOSE) up --build

## up-jetmon-v1: start the bridge against the Jetmon v1.1 Docker Compose MySQL
up-jetmon-v1: _ensure-env _ensure-shared-network
	JETMON_DSN="$(JETMON_V1_DSN)" \
	JETMON_WRITE="$(JETMON_V1_WRITE)" \
	JETMON_WRITE_DSN="$(JETMON_V1_WRITE_DSN)" \
	JETMON_BUCKET="$(JETMON_V1_BUCKET)" \
	JETMON_HISTORY_PATH="$(JETMON_V1_HISTORY_PATH)" \
	JETMON_HISTORY_POLL_INTERVAL="$(JETMON_V1_HISTORY_POLL_INTERVAL)" \
	JETMON_HISTORY_BOOTSTRAP="$(JETMON_V1_HISTORY_BOOTSTRAP)" \
	$(COMPOSE) up --build

_ensure-env:
	@if [ ! -f docker/.env ]; then cp docker/.env-sample docker/.env; fi

_ensure-shared-network:
	@docker network inspect jetmon-shared >/dev/null 2>&1 || docker network create jetmon-shared >/dev/null

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
up-local: _ensure-env _ensure-shared-network
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
	go build -o bin/jetmon-bridge ./cmd/jetmon-bridge

## test: run unit tests
test:
	go test ./...

## vet: run Go static checks
vet:
	go vet ./...

## check: run unit tests and static checks
check: test vet

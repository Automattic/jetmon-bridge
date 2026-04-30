# Docker

All Docker-related files live under `docker/`.

## Files

| File | Purpose |
|---|---|
| `docker/Dockerfile` | Multi-stage production image build |
| `docker/Dockerfile.dockerignore` | Build-context exclusions for `docker/Dockerfile` |
| `docker/docker-compose.yml` | Bridge service against an external Jetmon database |
| `docker/docker-compose.local.yml` | Local MySQL service and seeded test data |
| `docker/.env-sample` | Compose environment template |
| `docker/init.sql` | Local test schema and seed rows |

The Compose project name defaults to `jetmon-bridge` to avoid collisions with other repositories.

## Local Test Data

```bash
make up-local
curl http://localhost:7400/healthz
curl "http://localhost:7400/monitors?url=https://bench-target-01.example.com"
make down-local
```

`make up-local` creates `docker/.env` if it does not exist and starts MySQL with `docker/init.sql`.

## Real Jetmon Database

```bash
cp docker/.env-sample docker/.env
# Set JETMON_DSN in docker/.env
make up
```

Stop:

```bash
make down
```

## Jetmon v1.1 Compose Database

When Jetmon v1.1 is running from its own Compose stack, start that stack first and ensure the shared network exists:

```bash
cd ../jetmon/docker
cp .env-sample .env
docker network create jetmon-shared 2>/dev/null || true
docker compose up --build
```

Then start the bridge:

```bash
cd ../../jetmon-bridge
make up-jetmon-v1
```

Override the default DSN when Jetmon's Compose credentials differ:

```bash
make up-jetmon-v1 JETMON_V1_DSN='root:secret@tcp(jetmon-v1-mysql:3306)/jetmon_db'
```

## Persistent-History Smoke

After `make up-local`, change a seeded monitor's status, wait longer than the poll interval, change it back, then fetch events:

```bash
docker compose -p jetmon-bridge -f docker/docker-compose.yml --env-file docker/.env -f docker/docker-compose.local.yml exec mysql \
  mysql -uroot -pjetmon_test jetmon_db \
  -e "UPDATE jetpack_monitor_sites SET site_status=2,last_status_change=UTC_TIMESTAMP() WHERE blog_id=1002"

sleep 3

docker compose -p jetmon-bridge -f docker/docker-compose.yml --env-file docker/.env -f docker/docker-compose.local.yml exec mysql \
  mysql -uroot -pjetmon_test jetmon_db \
  -e "UPDATE jetpack_monitor_sites SET site_status=1,last_status_change=UTC_TIMESTAMP() WHERE blog_id=1002"

sleep 3

curl "http://localhost:7400/events?blog_id=1002&since=$(date -u -d '10 minutes ago' +%Y-%m-%dT%H:%M:%SZ)&until=$(date -u -d '10 minutes' +%Y-%m-%dT%H:%M:%SZ)"
```

Clean up local data:

```bash
make down-clean
```

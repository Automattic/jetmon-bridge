# Deployment

## Flags

| Flag | Default | Purpose |
|---|---|---|
| `-dsn` | required | MySQL DSN for Jetmon reads, normally a replica |
| `-addr` | `127.0.0.1:7400` | Listen address |
| `-read-timeout` | `5s` | Per-request database timeout |
| `-write` | `false` | Enable `POST /monitors` and `DELETE /monitors` |
| `-write-dsn` | required when `-write=true` | MySQL primary DSN for write mode |
| `-token` | empty | Bearer token required on all requests; empty disables auth |
| `-bucket` | `0` | Jetmon worker bucket assigned to new write-mode monitors |
| `-history-path` | `JETMON_HISTORY_PATH` or empty | SQLite path for bridge-owned persistent history |
| `-history-poll-interval` | `JETMON_HISTORY_POLL_INTERVAL` or `15s` | Persistent-history poll interval |
| `-history-bootstrap` | `JETMON_HISTORY_BOOTSTRAP` or `true` | Seed observed state once at startup |
| `-version` | false | Print version and exit |

## Systemd

First-time provisioning creates the service user, `/opt/jetmon-bridge`, the systemd unit, and the environment file template:

```bash
./scripts/provision.sh deploy@your-server
```

Before starting the service, edit `/opt/jetmon-bridge/env` and set at least `JETMON_DSN`:

```bash
ssh deploy@your-server 'sudo nano /opt/jetmon-bridge/env'
ssh deploy@your-server 'sudo systemctl start jetmon-bridge && sudo systemctl status jetmon-bridge'
```

Subsequent binary updates:

```bash
./scripts/deploy-prod.sh deploy@your-server
```

Logs:

```bash
ssh deploy@your-server 'journalctl -u jetmon-bridge -f'
```

## Auth And Exposure

Keep the listener on loopback unless a trusted private network or reverse proxy is in front of it. Set `-token` for any deployment where other hosts can reach the bridge, and treat it as required when write mode is enabled.

## Write Mode

Write mode is for uptime-bench provisioning. It requires:

- `-write=true`
- `-write-dsn` pointing at the Jetmon primary
- a `-bucket` value assigned to active Jetmon workers
- a bearer token in non-local environments

The write DSN only needs the permissions required for benchmark monitor rows in `jetpack_monitor_sites`.

## Persistent History

Persistent history is bridge-owned state. It does not write to Jetmon. In systemd deployments, place the SQLite file under `/opt/jetmon-bridge` unless you also update the unit's writable paths.

Set `-history-poll-interval` shorter than the Jetmon check interval. If Jetmon checks every five minutes, a 15 second or 30 second history poll is conservative.

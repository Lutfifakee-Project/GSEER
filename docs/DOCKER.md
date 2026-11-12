# GSEER — Full Self-Hosted Docker Stack

The full stack is **two containers**: PostgreSQL 17 and the GSEER
API + dashboard server.  Nothing else — no collector container (nothing
collects uninvoked; collection is an explicit command).  The server
container provisions itself with the hardened deployment order
(bootstrap → migrate → runtime grants → privilege audit) and serves
ONLY under the least-privilege `gseer_app` runtime credential — the
audit and the runtime-identity gate refuse an unsafe deployment with
exit 5 before any port binds (`docs/DEPLOYMENT.md`).

## 1. Startup (short workflow)

```
# 1. generate compose + .env (random DB password + random API token)
gseer setup --with-compose --full

# 2. build + start (db starts first; the server waits for a HEALTHY db)
docker compose -f docker-compose.full.yml up -d --build

# 3. verify
docker compose -f docker-compose.full.yml ps          # db: healthy, server: healthy
curl -s http://127.0.0.1:8443/api/v1/health           # {"status":"ok"}
curl -s http://127.0.0.1:8443/api/v1/version
```

Then use it (the API requires the generated token):

```
TOKEN=$(grep '^GSEER_TOKEN=' .env | cut -d= -f2-)
curl -s -H "Authorization: Bearer $TOKEN" 'http://127.0.0.1:8443/api/v1/search?q=port%3D%3D443'
# or the dashboard: http://127.0.0.1:8443/
```

> The `gseer` client-profile CLI cannot present a Bearer header (no
> token flag exists and its requests carry none), so a token-protected
> server is queried over HTTP as above, or from inside the compose
> network (below). Credential model details: `docs/DEPLOYMENT.md`.

Or query from inside the network (no token needed on loopback inside
the container):

```
docker compose -f docker-compose.full.yml exec server gseer search 'port == 443'
```

Load the labeled synthetic corpus (evaluation; NOT real data):

```
docker compose -f docker-compose.full.yml exec server gseer setup --load-fixtures
```

## 2. Security posture (what the template enforces)

- **DB publishes no host port.**  The database is reachable only on the
  compose-internal network from the `server` service — strictly
  stronger than a loopback bind.  Admin/backup access goes through
  `docker compose exec db psql ...`.
- **API publishes on the host loopback only** (`127.0.0.1:8443:8443`).
  Exposing a LAN address is an explicit operator edit — and the server
  *requires* the API token for any non-loopback bind
  (`E_CONFIG_INSECURE` otherwise); the generated `.env` carries a
  random token, and every client request sends it.
- **`.env` holds all secrets** (database password + the `gseer_app`
  runtime password + API token), written `0600`, never overwritten by a
  re-run.
- **Non-root runtime user** (uid 10001) in the image; no build tools in
  the final layer; `CGO_ENABLED=0` static binary.

## 3. Upgrade

Container deployments upgrade by **rebuilding the image** — not via
`gseer update` (the container binary is root-owned and read-only for
the runtime user; docs/UPDATE.md §1).

```
git pull                                  # or fetch the new release source
docker compose -f docker-compose.full.yml up -d --build
```

Schema migrations are part of the container's provisioning step (the
hardened deployment order: bootstrap → migrate → grants → audit); the
audit refuses an incomplete or unsafe schema before the server binds.
Downgrade = restore the previous image AND the database backup taken
before the upgrade (migrations are not auto-reversed).

## 4. Backup and restore

The index state lives in the `gseer-db-data` volume, owned by
PostgreSQL.  Backup with the server's own tooling (consistent, includes
everything: assets, observations, suppressions, collection bookkeeping):

```
# backup (custom format, compressed)
docker compose -f docker-compose.full.yml exec db \
  pg_dump -U gseer -Fc gseer > gseer-backup-$(date +%Y%m%d).dump

# restore into a fresh volume
docker compose -f docker-compose.full.yml down
docker volume rm gseer-db-data
docker compose -f docker-compose.full.yml up -d db
sleep 10   # wait for healthy: docker compose ... ps
cat gseer-backup-YYYYMMDD.dump | \
  docker compose -f docker-compose.full.yml exec -T db \
  pg_restore -U gseer -d gseer --clean --if-exists
docker compose -f docker-compose.full.yml up -d server
```

Bare-metal operators use the same `pg_dump`/`pg_restore` against the
published loopback port with the `.env` DSN.

## 5. Clean shutdown and recovery

```
docker compose -f docker-compose.full.yml stop     # SIGTERM → graceful drain (30s window)
docker compose -f docker-compose.full.yml start
docker compose -f docker-compose.full.yml down     # remove containers, KEEP the volume
```

Recovery scenarios:

| Symptom | Action |
| --- | --- |
| server unhealthy right after start | `docker compose logs server` — most commonly the DB was not healthy yet; the container restarts automatically (`restart: unless-stopped`) |
| `db` unhealthy | check the volume (`docker volume inspect gseer-db-data`) and disk space; PostgreSQL logs are in `docker compose logs db` |
| `.env` lost | secrets are unrecoverable by design — regenerate with `setup --with-compose --full` after moving the old volume aside, or restore from a known `.env` backup |
| migration failed mid-startup | `docker compose logs server`; repair with `gseer db migrate diagnose` / `force` run inside the container: `docker compose exec server gseer db migrate diagnose` |

## 6. Data and collection boundaries (unchanged in containers)

- No connector runs automatically: no daemon, no cron, no sidecar.
  `docker compose exec server gseer collect run --source <name>` is the
  operator's explicit action, gated by the same terms register
  (`docs/SOURCES.md`, `E_SOURCE_TERMS`).
- Suppression, fixture-purge isolation, and the asset-only honesty
  contract apply identically inside the container.
- RDAP/CT remain disabled pending provider authorization.

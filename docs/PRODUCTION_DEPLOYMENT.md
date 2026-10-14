# Sub2API production deployment

Verified 2026-10-15 Asia/Singapore. Stage C deployment is running with diagnostics disabled and the 15-minute health observation has passed. One explicitly approved gateway inference request remains pending before full Stage C acceptance. Recheck the live version and image before the next deployment.

For the subproxy integration rollout, see [the release and rollout plan](SUBPROXY_RELEASE_PLAN.md). Stage D enablement requires separate authorization.

- SSH: `ssh -o BatchMode=yes aoxtoken@20.195.40.145` (default port; existing local SSH key).
- Directory: `/home/aoxtoken/sub2api`.
- Compose services: sub2api, postgres, redis. Application bound to 127.0.0.1:8080.
- Application project: `sub2api`. Always use both `-f docker-compose.yml -f compose.subproxy.yml` for normal application operations. The base file retains the previous `.22` image; the override pins `.23` and explicitly sets `SUBPROXY_ENABLED=false`. A base-file-only recreate would revert the application.
- Public checks: https://api.aoxtoken.com/health, https://aoxtoken.com/login; unauthenticated /v1/models returns 401.
- Current version: v0.2.15-aox.0.0.23, source 41d5706d34ad87057129e5596e8a43ab4eda3163.
- Current image digest: sha256:bd969c74944c4b0d061de580af0f1f5e5bfd3fc9496412f137ab7875daa47d92.
- PostgreSQL: 18.6, `postgres@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873` (previously 18.4).
- Redis: 8.10.2, `redis@sha256:ba6e394f6acc2a695ef1b6944f161b9ca813711739be68319fa0db3470673f1d` (previously 8.8.0).
- Host: Ubuntu 24.04 LTS, kernel 7.0.0-1014-azure, Docker 29.8.1; system updates and reboot verified 2026-09-24.
- Data-service upgrade backup: `/home/aoxtoken/sub2api/backups/pre-data-services-20260923T182950Z` (database dump, global roles, cold PostgreSQL/Redis directories, compose/env, checksums and upgrade log). All checksums verified; old images retained. Do not restore data over a running service.
- Redis currently uses RDB persistence (`aof_enabled:0`); the existing multiline shell command does not apply its intended AOF flags. Review this separately before changing persistence or authentication.
- Previous image digest: sha256:a0c71587cf7b273abdf0da15fe458952248ebeba6c240eb7da7c434cfe76ee09 (v0.2.15-aox.0.0.22, source 2f6ab29bd1d58dc5decc8e081a94d2b30cbdd35b).
- Remote verified backup: `/home/aoxtoken/sub2api/backups/pre-v0.2.15-aox.0.0.23-20261014T171249Z` (0700 directory; current compose/override/env/config, nginx archive, database dump, checksum, restore catalog, table counts, startup and deployment records).
- Dump: 47,516,814 bytes, SHA-256 `d98a2aa5ee7dd6f842668f135fff0a600445e40b17176f789d9e56138dfb18d5`. Restored successfully in a separate PostgreSQL container with `--network none` and no published ports; all 100 public tables had matching row counts. Restore used `--no-owner --no-privileges`; this validates data/schema recovery, not restoration of cluster roles or grants.
- `.22` to `.23` migration-directory trees are identical; production migration records remained unchanged and startup logged no migration execution. The `.22` rollback does not require restoring the database.
- Historical deployment log (local evidence only): /Users/dalu/aiworkspace/codex/runtime/sub2api-aox14-production-deploy.log. Connection details and deployment procedure are maintained in this repository; this log is not required for deployment.

## Deployment procedure

1. Connect using the SSH command above. In the deployment directory, check the running application version/image, container health and free disk space.
2. Confirm the target release and immutable image digest, then pull that digest before interrupting service.
3. Create a dated backup directory under backups/ with restrictive permissions; preserve docker-compose.yml and .env. Do not commit environment files, credentials or database backups.
4. Stop only the sub2api service and confirm no other database clients are writing. Dump the sub2api database using pg_dump -U sub2api -d sub2api -Fc through the postgres service. Verify the dump is nonempty, validate its catalog using pg_restore --list, and record its SHA-256 checksum. Restore into an isolated database and verify recovery before recreating the application; never restore over the live database as part of validation.
5. Update the application image in the effective override, preserve the explicit diagnostics setting, and validate with `docker compose -p sub2api -f docker-compose.yml -f compose.subproxy.yml config --quiet`. Recreate only the application with the same file combination and `up -d --no-deps --no-build --force-recreate sub2api`.
6. Check container health, /app/sub2api -version, restart count, startup/migration errors, and the public endpoints above.
7. If deployment fails, review migration compatibility before changing the application image. When the old binary is compatible with the current data, restore the backed-up compose file and restart only sub2api. Retain the database backup; do not automatically restore the database.
8. Update this document with the verified version, digest, previous image and backup location.

When using a deployment script, upload it with scp and execute the remote file. Do not pipe a script into bash over SSH: Docker commands may consume the script's standard input.

## Subproxy deployment and Stage C evidence

- Subproxy `1.0.0`, source `9ea62d26ad969374b4dabcd07ff7f8c44f140e40`, image `ghcr.io/ihuhoo/subproxy@sha256:a62d87142145c8534094a29a6a9be70bb004ef6625cc206d3a5782f8394debe6`.
- Fixed project `subproxy`, version directory `/home/aoxtoken/subproxy/releases/1.0.0-9ea62d26a`. Use its `deploy/.env` and `deploy/compose.example.yml`, always with `--no-build` when starting. All nine rule files match the frozen revision; no custom overrides are active. Unverified targets remain disabled.
- Container runs as UID/GID 65532, read-only root, all capabilities dropped, no-new-privileges, no published ports. `subproxy-caller` is internal; `subproxy_egress` provides egress. The application retains `sub2api_sub2api-network` and also joins `subproxy-caller`.
- Production service token: `/home/aoxtoken/subproxy/secrets/service_token`. Independent HMAC: `/home/aoxtoken/sub2api/secrets/subproxy_hmac`. Each contains a separately generated 32-byte random value encoded as 64 hex characters. Directories are 0700, bind-mounted files 0444 for container access; secret backups are 0600 within the restricted backup directory. No secret values belong in this document or Git.
- First subproxy startup on October 13 failed because archive extraction under umask 077 left rule directories at 0700. Only the read-only rule/custom-rule directories were corrected to 0755; secret directories remain 0700. The container was recreated and verified healthy. Failure evidence is retained.
- GHCR read-only credential: `/home/aoxtoken/.ghcr-pull-token`, mode 0600, account `iHuHoo`, `read:packages` only as independently reviewed. Expires **2027-01-11 06:42 UTC**; rotate before expiry, log in using `--password-stdin`, and reverify digest access. Docker credentials are stored in `/home/aoxtoken/.docker/config.json` (0600; parent 0700). Do not copy a development token with write/delete scope.
- Application stop began at 2026-10-15 01:12:52 +08; `.23` became healthy at 01:14:22 +08. Stop-to-health elapsed 90.3 seconds; dump 11.0 seconds, isolated restore validation 68.7 seconds. PostgreSQL, Redis and subproxy IDs/start times/restart counts were unchanged.
- Public health/login/models checks returned 200/200/401. The existing administrator session survived reload; the UI displayed `.23` and retained the legacy quality-check controls. One previously authorized proxy completed the legacy check with five passing items. These network checks do not establish model inference or account entitlement.
- Observation completed from 01:14:22 to 01:29:22 +08 on 2026-10-15: all four services remained healthy with zero restarts. Nginx recorded 108 requests (107 HTTP 200, one expected anonymous HTTP 401, zero 5xx). Gateway-route requests were five HTTP 200 and the expected anonymous HTTP 401; these are not proof of completed inference. No completed inference usage records were observed after deployment at the final check, and no billable request was dispatched by the agent. One explicitly approved gateway inference request remains pending for full Stage C acceptance. Diagnostics remain disabled; Stage D has not been authorized.
- Deployment evidence: `/home/aoxtoken/sub2api-stagec-20261015/`; local copy `/Users/dalu/aiworkspace/codex/runtime/sub2api-stagec-20261015/`. The stopped restore-validation container and its isolated Docker volume retain recovery evidence; do not attach them to a production network.

For a `.23` failure requiring binary rollback, first confirm the same zero-migration premise still holds. Restore the reviewed `.22` base configuration and recreate only `sub2api` using that base file without the `.23` override. Do not restore a dump over the running database. Retain both images and backup materials through the 24-hour follow-up.

The 2026-09-24 deployment completed migrations 238b (moderation provenance), 239 (reasoning-effort pricing maps) and 240 (affiliate withdrawal idempotency). Verified all three migration records, four new columns and the valid operation-ID unique index. Application startup logged no ERROR/FATAL/PANIC entries; container healthy with zero restarts, public health/login returned 200 and unauthenticated models returned 401.

Migration 239 removes legacy group pricing keys after converting them to maps. A binary-only rollback to v0.2.7 cannot restore the old pricing behavior, and later map edits are not mirrored into the legacy channel column. Review/reverse pricing data explicitly or restore an appropriate backup after assessing intervening writes. See docs/upstream/v0.2.8-aox-review.md.

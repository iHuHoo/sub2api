# Sub2API production deployment

Verified 2026-09-24 Asia/Singapore.

- SSH: `ssh -o BatchMode=yes aoxtoken@20.195.40.145` (default port; existing local SSH key).
- Directory: `/home/aoxtoken/sub2api`.
- Compose services: sub2api, postgres, redis. Application bound to 127.0.0.1:8080.
- Public checks: https://api.aoxtoken.com/health, https://aoxtoken.com/login; unauthenticated /v1/models returns 401.
- Current version: v0.2.8-aox.0.0.15, source dcc1d13d6873de686fce79b925a5d400b29fbcfd.
- Current image digest: sha256:5927386ddab9db5742afe0242c92fa28cb62af5e292c425258ac8f07b58f0b5a.
- PostgreSQL: 18.6, `postgres@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873` (previously 18.4).
- Redis: 8.10.2, `redis@sha256:ba6e394f6acc2a695ef1b6944f161b9ca813711739be68319fa0db3470673f1d` (previously 8.8.0).
- Host: Ubuntu 24.04 LTS, kernel 7.0.0-1014-azure, Docker 29.8.1; system updates and reboot verified 2026-09-24.
- Data-service upgrade backup: `/home/aoxtoken/sub2api/backups/pre-data-services-20260923T182950Z` (database dump, global roles, cold PostgreSQL/Redis directories, compose/env, checksums and upgrade log). All checksums verified; old images retained. Do not restore data over a running service.
- Redis currently uses RDB persistence (`aof_enabled:0`); the existing multiline shell command does not apply its intended AOF flags. Review this separately before changing persistence or authentication.
- Previous image digest: sha256:51ceb325f0a7e0b72204323b46be30353d3c3a83f63db04765e6a2d414485957 (v0.2.7-aox.0.0.14).
- Remote verified backup: /home/aoxtoken/sub2api/backups/pre-v0.2.8-aox.0.0.15-20260923T191157Z (database dump, checksum, restore list, compose/env, migration snapshots and deployment/startup logs). Dump catalog and complete SQL decoding verified before deployment.
- Historical deployment log (local evidence only): /Users/dalu/aiworkspace/codex/runtime/sub2api-aox14-production-deploy.log. Connection details and deployment procedure are maintained in this repository; this log is not required for deployment.

## Deployment procedure

1. Connect using the SSH command above. In the deployment directory, check the running application version/image, container health and free disk space.
2. Confirm the target release and immutable image digest, then pull that digest before interrupting service.
3. Create a dated backup directory under backups/ with restrictive permissions; preserve docker-compose.yml and .env. Do not commit environment files, credentials or database backups.
4. Stop only the sub2api service. Dump the sub2api database using pg_dump -U sub2api -d sub2api -Fc through the postgres service. Verify the dump is nonempty, validate its catalog using pg_restore --list, and record its SHA-256 checksum.
5. Replace only the application image in docker-compose.yml with the verified digest. Run docker compose config --quiet, then docker compose up -d --no-deps sub2api.
6. Check container health, /app/sub2api -version, restart count, startup/migration errors, and the public endpoints above.
7. If deployment fails, review migration compatibility before changing the application image. When the old binary is compatible with the current data, restore the backed-up compose file and restart only sub2api. Retain the database backup; do not automatically restore the database.
8. Update this document with the verified version, digest, previous image and backup location.

When using a deployment script, upload it with scp and execute the remote file. Do not pipe a script into bash over SSH: Docker commands may consume the script's standard input.

The 2026-09-24 deployment completed migrations 238b (moderation provenance), 239 (reasoning-effort pricing maps) and 240 (affiliate withdrawal idempotency). Verified all three migration records, four new columns and the valid operation-ID unique index. Application startup logged no ERROR/FATAL/PANIC entries; container healthy with zero restarts, public health/login returned 200 and unauthenticated models returned 401.

Migration 239 removes legacy group pricing keys after converting them to maps. A binary-only rollback to v0.2.7 cannot restore the old pricing behavior, and later map edits are not mirrored into the legacy channel column. Review/reverse pricing data explicitly or restore an appropriate backup after assessing intervening writes. See docs/upstream/v0.2.8-aox-review.md.

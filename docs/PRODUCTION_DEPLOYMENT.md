# Sub2API production deployment

Verified 2026-09-21 Asia/Singapore.

- SSH: `ssh -o BatchMode=yes aoxtoken@20.195.40.145` (default port; existing local SSH key).
- Directory: `/home/aoxtoken/sub2api`.
- Compose services: sub2api, postgres, redis. Application bound to 127.0.0.1:8080.
- Public checks: https://api.aoxtoken.com/health, https://aoxtoken.com/login; unauthenticated /v1/models returns 401.
- Current version: v0.2.7-aox.0.0.14, source 9e9b78fad84313f85f14e91bccc6b0ccc1b8cf7a.
- Current image digest: sha256:51ceb325f0a7e0b72204323b46be30353d3c3a83f63db04765e6a2d414485957.
- Previous image digest: sha256:dbfc9b1bccc0a57a80009dc136bf475d7ffb9204ac8114e68fa0c4f3a08d011c (v0.2.5-aox.0.0.13).
- Remote verified backup: /home/aoxtoken/sub2api/backups/pre-v0.2.7-aox.0.0.14-20260920T170937Z (database dump, checksum, restore list, compose and env).
- Historical deployment log (local evidence only): /Users/dalu/aiworkspace/codex/runtime/sub2api-aox14-production-deploy.log. Connection details and deployment procedure are maintained in this repository; this log is not required for deployment.

## Deployment procedure

1. Connect using the SSH command above. In the deployment directory, check the running application version/image, container health and free disk space.
2. Confirm the target release and immutable image digest, then pull that digest before interrupting service.
3. Create a dated backup directory under backups/ with restrictive permissions; preserve docker-compose.yml and .env. Do not commit environment files, credentials or database backups.
4. Stop only the sub2api service. Dump the sub2api database using pg_dump -U sub2api -d sub2api -Fc through the postgres service. Verify the dump is nonempty, validate its catalog using pg_restore --list, and record its SHA-256 checksum.
5. Replace only the application image in docker-compose.yml with the verified digest. Run docker compose config --quiet, then docker compose up -d --no-deps sub2api.
6. Check container health, /app/sub2api -version, restart count, startup/migration errors, and the public endpoints above.
7. If deployment fails, restore the backed-up compose file and restart only sub2api. Retain the database backup; do not automatically restore the database. Review migration compatibility before rolling back.
8. Update this document with the verified version, digest, previous image and backup location.

When using a deployment script, upload it with scp and execute the remote file. Do not pipe a script into bash over SSH: Docker commands may consume the script's standard input.

The 2026-09-21 upgrade from v0.2.5 to v0.2.7 introduced no database migrations. Recheck migrations for each future release.

# Release checklist

The release workflow (`.github/workflows/release.yml`) runs on a `v*` tag and is
split into a read-only **verify** job (tests, vet, formatting, build) and a
restricted **publish** job. Day-to-day commits are verified locally with
`scripts/local-ci.sh`; GitHub CI is manual and release-time only.

## Before tagging

- [ ] `scripts/local-ci.sh` passed on the target commit.
- [ ] Optional full pre-release run: `gh workflow run ci.yml` and wait for
      `ci-gate` to pass.
- [ ] Weekly security workflow is green (or run it manually).
- [ ] `CHANGELOG.md` has an entry for the version with breaking changes and
      migration notes called out.
- [ ] Configuration additions are documented in `docs/configuration.md`.
- [ ] Dependency audit reviewed (`docs/development/license-policy.md`); no new
      denylisted license.
- [ ] `SECURITY.md` supported-versions table updated.

## Tag and publish

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The workflow then:

1. Verifies the tag is an ancestor of `main` and that the tagged commit's checks
   all passed.
2. Runs `go mod verify`, `go test -race ./...`, and `govulncheck`.
3. Builds server, worker, and admin for linux/darwin/windows (amd64+arm64).
4. Generates the SPDX SBOM and the third-party notices bundle.
5. Signs every artifact and the container image with cosign (keyless), attests
   build provenance, and publishes the GitHub release.

The `release` environment requires maintainer approval; no agent may publish.

## Verify a published release

```bash
gh release download v0.1.0 -D dist
cd dist && sha256sum -c checksums.txt

# Verify the SBOM
python3 - <<'PY'
import json
d = json.load(open("mage2mcp.sbom.spdx.json"))
print(d["spdxVersion"], len(d["packages"]), "packages")
PY

# Verify signatures (keyless: certificate identity must match the workflow)
cosign verify-blob --bundle mage2mcp-server_linux_amd64.bundle \
  --certificate-identity-regexp 'https://github.com/shuaiZend/mage2mcp/.github/workflows/release.yml@refs/tags/v0.1.0' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  mage2mcp-server_linux_amd64

cosign verify ghcr.io/shuaizend/mage2mcp:v0.1.0 \
  --certificate-identity-regexp 'https://github.com/shuaiZend/mage2mcp/.github/workflows/release.yml@refs/tags/v0.1.0' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Dry-run evidence (2026-11-19)

The dry run at run `44378145844` produced 15 binaries plus `checksums.txt` and
an SPDX 2.3 SBOM with 339 packages (including the MCP Go SDK). Downloading the
artifact and running `sha256sum -c checksums.txt` reported **OK** for every
file.

## Rollback

A release cannot be unpublished safely once consumers may have pulled it. If a
defect is found, publish a new patch release and mark the previous one as
superseded in the release notes; do not move or delete the tag. Database
rollback is a restore from backup (see [operations/backup-restore.md](operations/backup-restore.md)).

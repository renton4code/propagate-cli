# Propagate — Open Source Plan

This document proposes how to open-source the **CLI and backend application code** while keeping production infrastructure, live secrets, and operator-only workflows private. It covers repository strategy, MIT licensing, free distribution, and risks to resolve before launch.

**Status:** Draft for review  
**Scope:** `packages/cli/`, `packages/backend/`, required shared crypto (`packages/shared/secretcrypto/`), install assets, and product docs  
**Out of scope:** production secrets, cloud credentials, Terraform state/workspace internals, and operator-only deployment runbooks

---

## 1. Goals

| Goal | Why |
| --- | --- |
| Publish CLI source under a permissive license | **MIT** — short, familiar, and aligned with a fully free project; users and security reviewers need source access |
| Ship binaries for common platforms | Most users will not install Go; onboarding must be one command |
| Open backend source | Publish API and backend implementation for transparency, contribution, and self-hosting |
| Keep the project fully free | No paid tiers, subscriptions, commercial upsell, or monetization plan |
| Preserve clean public git history | No leaked GCP project IDs, Terraform state paths, `.env` examples with real URLs, or private operational runbooks in the public repo |
| Stay compatible with free hosted/community Propagate | OSS CLI must talk to the same HTTPS API contract; version skew must be managed |
| Document for security review | Public **whitepaper**, **contract**, **CLI**, and backend architecture docs with sensitive operational details redacted |

---

## 2. What goes public vs stays private

### 2.1 Public repository contents

```
propagate-cli/                 # new public repo (suggested name)
├── LICENSE                    # MIT
├── README.md                  # install + quickstart
├── cmd/propagate/main.go
├── internal/                  # current packages/cli/internal/*
├── backend/                   # exported from packages/backend (app code only)
├── pkg/ or shared/            # packages/shared/secretcrypto
├── scripts/
│   └── install.sh             # source of truth; also published to landing (see §6.2)
├── .github/workflows/
│   ├── ci.yml                 # go test, lint
│   └── release.yml            # cross-compile + GitHub Release
├── docs/
│   ├── README.md              # reading order; entry point into whitepaper + lightweight docs
│   ├── whitepaper.mdx         # security narrative: scenarios, workflows, trust boundaries
│   ├── contract/              # lightweight normative specs (linked from whitepaper)
│   │   ├── api.md
│   │   ├── crypto.md
│   │   ├── propagate-yaml.md
│   │   └── versioning.md
│   ├── cli/                   # lightweight CLI reference (linked from whitepaper)
│   │   ├── commands.md
│   │   ├── output.md          # JSON, exit codes, --dry-run, --no-color
│   │   └── install.md
│   └── self-hosting.md        # run your own API instance and point CLI at it
```

### 2.2 Stays private (ops-only material)

- Cloud credentials, service-account keys, and secret values (`.env`, KMS key IDs, tokens)
- Terraform state/backends, workspace identifiers, and internal-only infra account mappings
- Production-only deployment workflows and incident runbooks
- Abuse-detection/anti-automation operational thresholds and private response playbooks

### 2.3 Shared boundary (must be public)

`packages/shared/secretcrypto` defines envelope algorithms the CLI and API both implement (`propagate-envelope-x25519-aesgcm-v1`, scope key handling). This **must** ship in the public repo or be vendored into CLI — it is not secret, and hiding it blocks auditability.

**Recommendation:** Move `secretcrypto` into the public product repo as `internal/crypto/` or top-level `crypto/` and drop the separate `propagate/shared` module in the public tree.

### 2.4 Public documentation architecture

Private planning docs can remain internal, but backend architecture and API behavior should be published in sanitized public docs. Keep a **layered doc set**: one whitepaper for security-sensitive narrative, plus contract, CLI, and backend docs that cross-link (and can also be read standalone).

```
docs/README.md  ──►  docs/whitepaper.mdx  ──►  scenarios, workflows, trust model
                              │
                              ├──►  docs/contract/*   (normative specs)
                              └──►  docs/cli/*        (command reference)
```

#### Whitepaper (`docs/whitepaper.mdx`)

Audience: users, security reviewers, team leads deciding whether to adopt Propagate.

Purpose: explain **why** Propagate works the way it does and walk through **security-sensitive scenarios and workflows** in plain language. This complements backend implementation docs with narrative and trust boundaries.

Suggested sections:

| Section | Contents |
| --- | --- |
| Executive summary | Security goals and the core local-encryption model |
| Security model | Identity (`~/.propagate`), envelope encryption, what the cloud can and cannot see, metadata in Git vs ciphertext in cloud |
| Workflows | End-to-end flows: first setup, git-mediated join, PIN invite join (including relay trust exception), config push approval, env pull vs `propagate run`, prod guardrails |
| Limitations and scenarios | Lost private key, revocation limits, duplicate handles, tracked `.env`, AI agent boundaries, install integrity, conflict/revision rejection |

Whitepaper sections should **link** to contract/cli docs for details (“see [crypto contract](../contract/crypto.md)”) rather than duplicating normative specs.

**Sources to rewrite from:** existing PRD, API guide, and backend implementation docs after redacting secrets and operator-only internals.

#### Contract docs (`docs/contract/`)

Audience: integrators, contributors implementing or auditing the client, self-hosters at the API boundary.

Purpose: lightweight, **normative** specifications — stable names, algorithms, file shapes, compatibility rules. Keep these concise and implementation-agnostic where possible.

| File | Contents |
| --- | --- |
| `api.md` | HTTPS API surface at behavioral level: signed requests, idempotency, error categories, version headers — no Supabase/stored-function detail |
| `crypto.md` | Public algorithms: scope keys, value encryption, envelopes, digests (`hmac-sha-256:v1:`), relay invite at **client-visible** boundary only |
| `propagate-yaml.md` | Config file schema, safe declarations, pending joins, sensitivity rules |
| `versioning.md` | CLI semver vs API compatibility matrix; deprecation policy |

**Sources to rewrite from:** `propagate-api-implementation-guide.md`, `packages/shared/secretcrypto`, backend handlers, and CLI client code.

#### CLI docs (`docs/cli/`)

Audience: day-to-day developers and agent authors running commands.

Purpose: lightweight **CLI reference** — commands, flags, output contracts, install paths.

| File | Contents |
| --- | --- |
| `commands.md` | Command groups, flags, non-interactive behavior, approval requirements |
| `output.md` | Human output style, `--json` schemas, exit codes, `--dry-run` |
| `install.md` | Install script URL on landing, manual GitHub Release download, checksum verification, PATH setup |

**Sources to adapt:** `propagate-cli-implementation-guide.md` (remove monorepo paths), `docs/cli-scenarios.mdx` if kept as examples linked from `workflows.md`.

#### What stays private (doc mapping)

| Private document | Public replacement |
| --- | --- |
| `propagate-technical-design.md` | Publish sanitized architecture docs; keep only secrets/ops runbooks private |
| `propagate-prd.md` | Informs whitepaper and roadmap docs; keep strategy-only sections private |
| `propagate-api-implementation-guide.md` | `docs/contract/api.md` + backend architecture docs |
| `propagate-cli-implementation-guide.md` | Adapted into `docs/cli/*` |

Do not copy secrets or environment-specific internals during export — **author public docs explicitly** and sanitize examples.

---

## 3. Repository and git history strategy

**Requirement:** clean public history — no accidental secrets or environment-specific infra identifiers.

### 3.1 Recommended approach: public repo via filtered export

Open the product codebase (CLI + backend), but avoid exposing historical secrets/config churn that lived in old commits.

**Steps:**

1. Create empty GitHub repo `github.com/<org>/propagate` (or `propagate-cli`).
2. Use `git filter-repo` on a local clone to extract:
   - `packages/cli/**`
   - `packages/backend/**` (excluding infra/secret folders)
   - `packages/shared/secretcrypto/**`
   - docs, install scripts, public CI
   - **Exclude:** secrets, infra state/workspace internals, and private ops runbooks
3. Rewrite paths to flat layout (`cmd/`, `internal/`, not `packages/cli/...`).
4. Scrub commit messages if they reference internal infra or technical-design content.
5. Run secret scan (`gitleaks`, `trufflehog`) on the filtered repo before first push.
6. Verify export: no secret material appears in history or working tree.
7. Tag `v0.1.0` (or current version) as first public release.
8. Move to **public-first development** for CLI and backend; reserve private repos for infra/secrets only.

### 3.2 Ongoing sync model

| Direction | Mechanism |
| --- | --- |
| Product feature work | Develop in public repo (`cli` + `backend`) with standard PR flow |
| Infra/ops changes | Keep in private infra repo(s); reference released public tags where needed |
| Releases | Tag on **public** repo; deployments consume signed release artifacts |

Alternative: split into separate public repos (`propagate-cli`, `propagate-backend`) if contributor velocity or release cadence diverges significantly.

### 3.3 What to remove before export

- Any committed credentials, internal hostnames, or production account identifiers
- Environment-specific examples that imply real projects/tenants
- Internal-only deployment instructions that depend on private infrastructure

---

## 4. Licensing

### 4.1 License choice

**MIT License** — chosen for the public product repo.

Rationale:

- Simple, permissive, and familiar to users and contributors
- Aligned with the project being fully free and published without commercial intent
- Compatible with typical CLI dependencies, including the largely MIT-licensed Bubble Tea / Charm stack

Add at repo root:

- `LICENSE` — full MIT License text (copyright holder + year)

Keep the landing page, README, and release badges aligned on **MIT License** before public launch.

### 4.2 What the license does *not* cover

- **Trademark:** “Propagate” name and logo — add a brief trademark note only if needed for project identity
- **Hosted/community API:** any default hosted endpoint should remain free to use. Availability or acceptable-use policies can be documented separately, but this plan does not include a paid hosted service, subscription tier, or commercial offering.

### 4.3 Contributor agreement

For external PRs:

- `CONTRIBUTING.md` — DCO sign-off (`Signed-off-by`) is enough for MIT at small scale
- No CLA planned

---

## 5. Go module and naming

Current module: `propagate/cli` (local monorepo path; rename before public export).

Before public launch, set a proper GitHub module path for source and contributors:

```text
module github.com/<org>/propagate
```

Move entrypoint to:

```text
github.com/<org>/propagate/cmd/propagate
```

End users install **prebuilt release binaries** (see §6), not `go install`. The public module path is for cloning, contribution, and reproducible CI builds.

Embed version at build time:

```go
var Version = "dev" // ldflags: -X main.Version=v1.2.3
```

---

## 6. Distribution

Two free install paths, one release artifact set. **Single source of truth:** GitHub Release assets built by CI.

### 6.1 Platform matrix

All release artifacts are free to download, inspect, mirror, and redistribute under the MIT License.

| OS | Arch | Artifact name |
| --- | --- | --- |
| darwin | arm64, amd64 | `propagate_<version>_darwin_arm64.tar.gz` |
| linux | arm64, amd64 | `propagate_<version>_linux_amd64.tar.gz` |
| windows | amd64 | `propagate_<version>_windows_amd64.zip` |

Each release publishes:

- One archive per platform row above, containing the `propagate` binary
- `SHA256SUMS` at the release root covering all archives

Optional later: `linux musl` static builds for Alpine/containers.

Release asset URL pattern:

```text
https://github.com/<org>/propagate/releases/download/v1.0.0/propagate_1.0.0_linux_amd64.tar.gz
```

### 6.2 Option A — Shell install script (recommended)

**Hosted URL (canonical for users):**

```bash
curl -fsSL https://propagatecli.com/install.sh | sh
```

The install script is served as a **static asset from the landing site** at [https://propagatecli.com/](https://propagatecli.com/) (e.g. `packages/landing/public/install.sh`, deployed via Netlify). Docs, README, and the landing page should all use this URL — not `raw.githubusercontent.com`.

**Source and publish flow:**

| Location | Role |
| --- | --- |
| Public CLI repo `scripts/install.sh` | Version-controlled source; reviewed in OSS PRs |
| Landing `public/install.sh` | Static file deployed to `https://propagatecli.com/install.sh` |
| Release process | On each CLI release, copy or sync `scripts/install.sh` into landing and deploy (or automate in CI) |

Optional later: version-pinned static URLs such as `https://propagatecli.com/install/v1.0.0.sh` for teams that want an immutable script URL per release.

**Script responsibilities:**

1. Detect OS/arch (`uname -s`, `uname -m`).
2. Resolve CLI version: pinned default in script body (updated each release) or `latest` from GitHub Releases API.
3. Download the matching **binary** from **GitHub Releases** (never from the landing site or mutable branch URLs).
4. Verify `sha256sum` against the release `SHA256SUMS` file.
5. Install to `~/.propagate/bin` or `/usr/local/bin` (with `--prefix`).
6. Print PATH hint and `propagate version`.

**Security requirements (non-optional):**

- Binaries always from GitHub Releases + `SHA256SUMS`; landing hosts **only the script**, not platform binaries
- Checksums verified by default; `--insecure` only for debugging
- Document that piping curl to sh is a trust decision — point to Option B for manual download
- Landing deploy should serve `install.sh` with `Content-Type: text/plain` and cache headers appropriate for an install entrypoint (avoid aggressive stale cache after releases)
- Consider **cosign** / GitHub artifact attestations for release binaries (phase 2)

**Challenges:**

- Landing and CLI release pipelines must stay in sync when `install.sh` changes
- Corporate proxies may block the script — Option B remains the fallback
- macOS Gatekeeper: users may need `xattr -d com.apple.quarantine` — document in `docs/cli/install.md`
- Script must not require backend repo paths or private URLs

### 6.3 Option B — Manual download from GitHub Releases

**User experience:**

1. Open [GitHub Releases](https://github.com/<org>/propagate/releases) for the desired version.
2. Download the archive matching OS and architecture (see §6.1).
3. Verify the checksum against `SHA256SUMS` on the same release page.
4. Extract the binary and install it on `PATH` (e.g. `~/.local/bin`, `/usr/local/bin`).

**Example (Linux amd64):**

```bash
VERSION=v1.0.0
curl -LO "https://github.com/<org>/propagate/releases/download/${VERSION}/propagate_1.0.0_linux_amd64.tar.gz"
curl -LO "https://github.com/<org>/propagate/releases/download/${VERSION}/SHA256SUMS"
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf "propagate_1.0.0_linux_amd64.tar.gz"
install -m 755 propagate ~/.local/bin/
propagate version
```

**When to use:**

- Security teams that forbid `curl | sh`
- Air-gapped or proxy-restricted environments (download on another machine, transfer archive)
- Windows users who prefer a zip from the Releases UI
- Auditors who want an explicit artifact URL and checksum step

**Documentation requirements:**

- `README.md` links to Releases and shows one full manual example per OS family
- `docs/cli/install.md` lists every artifact name, checksum workflow, and Gatekeeper/quarantine notes
- Each release page repeats the platform matrix in release notes (copy-paste friendly)

**Out of scope for v1:** `go install`, Homebrew, npm wrappers, or other third-party package registries.

### 6.4 Default API URL for OSS users

The CLI has a production default and still allows explicit overrides through flags, environment, and the identity profile.

**Decision for public OSS builds:** default to the free hosted/community Propagate API at **`https://api.propagatecli.com/`**.

| Surface | Value |
| --- | --- |
| Baked binary default | CLI fallback value `https://api.propagatecli.com/` |
| Identity profile | Set `default_api_url` to `https://api.propagatecli.com/` on first identity creation when no override is configured |
| Resolution order | `--api-url` flag → `PROPAGATE_API_URL` env → profile `default_api_url` → baked binary default |

Self-hosters and staging can still override via `--api-url`, `PROPAGATE_API_URL`, or profile `default_api_url`. Document override paths in README and `docs/self-hosting.md`. Do not describe this endpoint as a paid tier or managed-service upsell.

---

## 7. Release and CI pipeline (public repo)

### 7.1 Continuous integration (every PR)

- `go test ./...`
- `go vet`, staticcheck (optional)
- Build smoke test all target GOOS/GOARCH
- Secret scan on diff

### 7.2 Release workflow (on tag `v*`)

1. Cross-compile with `goreleaser` or plain `go build` matrix
2. Generate `SHA256SUMS`
3. Create GitHub Release with artifacts and platform matrix in release notes
4. (Optional) Sync `scripts/install.sh` to landing `public/install.sh` and deploy `https://propagatecli.com/install.sh`

### 7.3 Versioning

- Semver for CLI (`v1.0.0`)
- CLI sends version in signed requests (already in design) — API must reject or warn on unsupported old CLIs
- Document compatibility matrix in `docs/contract/versioning.md` (CLI min version vs API version header)

---

## 8. Security and trust documentation (public)

Public security content lives primarily in **`docs/whitepaper.mdx`**, with normative detail in **`docs/contract/`** and operational detail in **`docs/cli/`**. The whitepaper should cover at minimum:

- Private keys stay in `~/.propagate` and permission expectations
- What metadata the cloud and Git-backed config expose (variable names, paths, handles)
- Security-sensitive workflows: git-mediated join vs PIN invite (including relay trust exception at user-visible boundary)
- `propagate env pull` vs `propagate run` trade-offs
- Revocation limits and lost-key recovery
- AI agent guardrails (guidance is not access control)
- Install script at `https://propagatecli.com/install.sh` (landing static); binaries from GitHub Releases with `SHA256SUMS` verification

`SECURITY.md` at repo root remains the vulnerability reporting channel (email or GitHub private advisory). Link to the whitepaper from `README.md` for adopters doing security review.

---

## 9. Pre-launch checklist

### Legal / org

- [ ] Add `LICENSE` (MIT), `SECURITY.md`, `CONTRIBUTING.md`
- [ ] Keep landing page, README, and release badges aligned on MIT
- [ ] Trademark / repo org naming (`propagate` vs `propagate-cli`)

### Repository

- [ ] Filter-repo export; gitleaks clean
- [ ] Module path `github.com/<org>/propagate`
- [ ] Remove backend dotenv path resolution from production code paths
- [ ] Verify no secrets or internal infra identifiers appear in history
- [ ] Author public doc set: `docs/whitepaper.mdx`, `docs/contract/`, `docs/cli/`, backend architecture docs

### Build / release

- [ ] `release.yml` cross-compile matrix
- [ ] `install.sh` in public repo; publish static copy to `https://propagatecli.com/install.sh` via landing deploy
- [ ] Manual GitHub Release download documented in README and `docs/cli/install.md`

### Product

- [x] Bake default API URL `https://api.propagatecli.com/` into OSS builds and identity `default_api_url`; document overrides
- [ ] Landing page install commands use `curl -fsSL https://propagatecli.com/install.sh | sh`
- [ ] API version compatibility policy

### Private operations

- [ ] Keep deploy credentials, infra state, and incident runbooks private
- [ ] Document boundary between public product code and private operations code

---

## 10. Challenges and decisions for you

These are the main pushbacks to resolve before flipping the repo public.

### 10.1 Clean history vs convenience

**Challenge:** A filtered export is a one-time cost but avoids forever explaining why old commits contain deleted sensitive config.  
**Question:** Open the current monorepo after cleanup, or split into public product + private infra repos before launch?

### 10.2 Open backend and sustainability

**Challenge:** Publishing backend code increases transparency and trust, and the project should avoid drifting into a commercial-service roadmap.  
**Question:** What is the lightweight maintenance plan for a fully free project: public issues, contributor review, modest free hosted capacity, and clear self-hosting docs?

### 10.3 Hosted cloud default in OSS binary

**Decision:** OSS builds default to **`https://api.propagatecli.com/`** (baked binary default and profile `default_api_url`) as a free hosted/community endpoint for zero-setup onboarding; self-hosters override via `--api-url`, `PROPAGATE_API_URL`, or profile.

**Residual consideration:** Document overrides clearly in README and `docs/self-hosting.md` so self-hosters are not surprised by the default.

### 10.4 License

**Decision:** Public product repo uses **MIT** (see §4). Align landing page and README license badges.

### 10.5 `curl | sh` trust model

**Challenge:** Security-conscious teams discourage pipe-to-shell; secrets-tool users are especially skeptical.  
**Question:** Will you invest in cosign attestations early, or ship checksums-only v1 and add signing in v2?  
**Mitigation:** Option B (manual GitHub Release download + `SHA256SUMS`) is a first-class path, not a footnote. Script is served from `https://propagatecli.com/install.sh` (landing), not GitHub raw URLs.

### 10.6 Public backend, private operations boundary

**Decision:** Backend source is public, but production secrets, infra state, and internal runbooks remain private.

**Implication:** OSS contributors can audit API and storage logic, while operational attack-surface details stay in private ops docs.

**Residual risk:** Boundary drift over time (private details reintroduced in examples, tests, or docs). Enforce with CI secret scans and doc review checklist.

### 10.7 Shared crypto module ownership

**Challenge:** `packages/shared/secretcrypto` is a critical boundary for both backend and CLI behavior.  
**Question:** Keep it in-tree in one repo for atomic changes, or publish a versioned `propagate-crypto` module to support independent release cycles?

### 10.8 E2E testing story

**Challenge:** Once backend is open, contributors need a reproducible integration path without production access.  
**Question:** Standardize local e2e with docker-compose/test fixtures, or provide a public staging API for CI validation?

### 10.9 Identity and account recovery

**Challenge:** PRD already notes lost keys are unrecoverable. Open sourcing increases scrutiny on local key storage (`~/.propagate` permissions, no keychain in MVP).  
**Question:** Is MVP filesystem-only identity acceptable for public launch, or delay OSS until keychain mode ships?

---

## 11. Suggested phased rollout

| Phase | Deliverable |
| --- | --- |
| **Phase 0 — Prep (internal)** | Module rename, scrub secret-bearing history, add LICENSE, draft whitepaper + contract + CLI + backend docs, gitleaks |
| **Phase 1 — Staging export** | Filter-repo to staging org; internal review of history, docs, and secret scanning gates |
| **Phase 2 — Public source** | Push public repo, CI green, contribution docs published |
| **Phase 3 — First release** | `v0.x.0`, GitHub Release assets, `install.sh` on landing + public repo, manual download docs |
| **Phase 4 — Hardening** | cosign, provenance attestations, package-manager integrations |

---

## 12. README install section (target copy)

```markdown
## Install

Prebuilt binaries are published on [GitHub Releases](https://github.com/<org>/propagate/releases).

### Install script (recommended)

    curl -fsSL https://propagatecli.com/install.sh | sh

Hosted statically on [propagatecli.com](https://propagatecli.com/). The script downloads the release binary from GitHub Releases and verifies `SHA256SUMS`.

### Manual download

1. Download the archive for your OS/arch from the [v1.0.0 release](https://github.com/<org>/propagate/releases/tag/v1.0.0).
2. Verify against `SHA256SUMS` on the same page.
3. Extract and place `propagate` on your `PATH`.

See [docs/cli/install.md](docs/cli/install.md) for per-platform examples.

## Quick start

    propagate quickstart --handle you@example.com --team-name "My Team" ...

The CLI defaults to `https://api.propagatecli.com/`. Override with `PROPAGATE_API_URL` or `--api-url` for self-hosted APIs.

## Documentation

- [Security whitepaper](docs/whitepaper.mdx) — trust model, sensitive workflows, scenarios
- [Contract specs](docs/contract/) — API, crypto, `propagate.yaml`, versioning
- [CLI reference](docs/cli/) — commands, output, install
```

---

## 13. Summary recommendation

1. **Open product source (CLI + backend)** in a cleaned public repo using **filter-repo** to remove sensitive history.  
2. **MIT** + `LICENSE`; keep landing page and README license text aligned.  
3. **Distribution:** GitHub Releases for binaries; `curl -fsSL https://propagatecli.com/install.sh | sh` (landing static) + manual download + `SHA256SUMS`.  
4. **Keep crypto and API contracts public**, and publish backend architecture docs; keep only operational secrets/runbooks private.  
5. **Default API URL** `https://api.propagatecli.com/` in OSS builds and `default_api_url`; keep it free, and document overrides for self-hosters.  
6. **Remove implicit env shortcuts** from CLI paths used in production and CI.  
7. **Adopt public-first development** for product code; isolate infra/ops code in private repos.

---

*Next step:* Review §10 decisions, then execute Phase 0 cleanup before any public push.

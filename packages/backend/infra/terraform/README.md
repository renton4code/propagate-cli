# Propagate Cloud Run Terraform

Terraform stack for the Propagate API on Google Cloud Run.

## Resources

- Required Google Cloud APIs
- Artifact Registry Docker repository
- Cloud Run runtime service account
- Secret Manager secret containers for runtime configuration
- Cloud Run v2 service
- Optional public `roles/run.invoker` binding

## Secrets

Terraform manages the **secret containers** (`google_secret_manager_secret`) but never the **secret values** (versions). Values are populated separately, from CI or `gcloud`:

```bash
printf '%s' "$PROPAGATE_DATABASE_URL" | gcloud secrets versions add propagate-database-url --data-file=-
```

Cloud Run mounts every secret with `version = "latest"` (see `main.tf`), so a valid, enabled `latest` version must exist before the service can start a revision.

### Adding a new runtime secret

Follow this order to avoid the failure modes described below. Do **not** run `gcloud secrets create` yourself — let Terraform own the container.

1. **Create the empty container first.** Add the secret ID to `secret_ids` only (leave it out of `secret_env` for now) and apply. Terraform creates the container and the IAM binding.

   ```hcl
   secret_ids = [
     "propagate-database-url",
     "propagate-new-secret", # new
   ]
   ```

2. **Add the value.** Now that the container exists, create its first version:

   ```bash
   printf '%s' "$VALUE" | gcloud secrets versions add propagate-new-secret --data-file=-
   ```

3. **Wire it into Cloud Run.** Add the env mapping to `secret_env` and apply. Because a `latest` version already exists, the new revision starts cleanly.

   ```hcl
   secret_env = {
     PROPAGATE_DATABASE_URL = "propagate-database-url"
     PROPAGATE_NEW_SECRET   = "propagate-new-secret" # new
   }
   ```

Doing steps 1 and 3 in a single apply is what causes the "version not found" error: Terraform creates the empty container and updates Cloud Run to mount `latest` in the same run, before any version exists.

### Secret gotchas (learned the hard way)

- **Never `gcloud secrets create` a Terraform-managed secret.** Terraform's `create` then fails with `409 already exists`. If it already happened, either delete the manual secret (no versions worth keeping) or adopt it with an `import` block:

  ```hcl
  import {
    to = google_secret_manager_secret.runtime["propagate-new-secret"]
    id = "projects/PROJECT_ID/secrets/propagate-new-secret"
  }
  ```

- **`latest` resolves to the highest version number, even if it is disabled or destroyed** — it does *not* fall back to an older enabled version. To roll a value back, add a **new** version with the old value; never destroy/disable the newest version expecting a fallback:

  ```bash
  gcloud secrets versions access 1 --secret=propagate-database-url \
    | gcloud secrets versions add propagate-database-url --data-file=-
  ```

- **Fixing a secret does not heal a failed revision.** Cloud Run revisions are immutable, so a revision that failed because `latest` was missing/broken stays failed. After fixing the secret, force a fresh revision:

  ```bash
  gcloud run services update propagate-api --region=REGION --revision-suffix=secretfix1
  ```

- **Keep `secret_ids` and `secret_env` in sync.** Every value in `secret_env` must have a matching entry in `secret_ids` so its container gets created (see `local.secret_ids` in `main.tf`).

## Usage

```bash
cp .terraform.tfvars.example terraform.tfvars
terraform init
terraform plan
terraform apply
```

Build and push an API image before applying or update `image` to an existing image:

```bash
gcloud builds submit ../.. \
  --tag us-central1-docker.pkg.dev/$PROJECT_ID/propagate/propagate-api:latest
```

For a brand-new project, apply the API enablement and Artifact Registry repository first, then build and push the image, then apply the full stack:

```bash
terraform apply \
  -target='google_project_service.required' \
  -target='google_artifact_registry_repository.api'
```

The current API image must listen on `PORT`; Cloud Run sets it automatically.

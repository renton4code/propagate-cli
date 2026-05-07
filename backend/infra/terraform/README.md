# Propagate Cloud Run Terraform

Terraform stack for the Propagate API on Google Cloud Run.

## Resources

- Required Google Cloud APIs
- Artifact Registry Docker repository
- Cloud Run runtime service account
- Secret Manager secret containers for runtime configuration
- Cloud Run v2 service
- Optional public `roles/run.invoker` binding

Secret values are not stored in Terraform. Populate Secret Manager versions separately, for example from CI:

```bash
printf '%s' "$PROPAGATE_DATABASE_URL" | gcloud secrets versions add propagate-database-url --data-file=-
```

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

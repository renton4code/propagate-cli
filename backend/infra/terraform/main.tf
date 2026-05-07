locals {
  labels = merge(
    {
      app         = "propagate"
      component   = "api"
      environment = var.environment
      managed_by  = "terraform"
    },
    var.labels
  )

  secret_ids = setunion(var.secret_ids, toset(values(var.secret_env)))

  required_services = toset([
    "artifactregistry.googleapis.com",
    "cloudbuild.googleapis.com",
    "iam.googleapis.com",
    "logging.googleapis.com",
    "run.googleapis.com",
    "secretmanager.googleapis.com",
  ])

  runtime_env = {
    PROPAGATE_API_ENV                 = var.environment
    PROPAGATE_API_VERSION             = var.api_version
    PROPAGATE_MIN_CLI_VERSION         = var.min_cli_version
    PROPAGATE_REQUEST_SKEW_SECONDS    = tostring(var.request_skew_seconds)
    PROPAGATE_MAX_BODY_BYTES          = tostring(var.max_body_bytes)
    PROPAGATE_LOG_LEVEL               = var.log_level
    PROPAGATE_RECOMMENDED_CLI_VERSION = var.recommended_cli_version
  }

  filtered_runtime_env = {
    for key, value in local.runtime_env : key => value
    if value != ""
  }
}

resource "google_project_service" "required" {
  for_each = local.required_services

  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
}

resource "google_artifact_registry_repository" "api" {
  project       = var.project_id
  location      = var.region
  repository_id = var.artifact_registry_repository_id
  description   = "Docker images for the Propagate API"
  format        = "DOCKER"
  labels        = local.labels

  depends_on = [
    google_project_service.required["artifactregistry.googleapis.com"],
  ]
}

resource "google_service_account" "api" {
  project      = var.project_id
  account_id   = "${var.service_name}-runtime"
  display_name = "Propagate API Cloud Run runtime"
  description  = "Runtime identity for the Propagate API Cloud Run service."

  depends_on = [
    google_project_service.required["iam.googleapis.com"],
  ]
}

resource "google_secret_manager_secret" "runtime" {
  for_each = local.secret_ids

  project   = var.project_id
  secret_id = each.value
  labels    = local.labels

  replication {
    auto {}
  }

  depends_on = [
    google_project_service.required["secretmanager.googleapis.com"],
  ]
}

resource "google_secret_manager_secret_iam_member" "api_secret_access" {
  for_each = google_secret_manager_secret.runtime

  project   = each.value.project
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.api.email}"
}

resource "google_cloud_run_v2_service" "api" {
  project             = var.project_id
  name                = var.service_name
  location            = var.region
  ingress             = var.ingress
  deletion_protection = var.deletion_protection
  labels              = local.labels

  template {
    service_account                  = google_service_account.api.email
    timeout                          = "${var.request_timeout_seconds}s"
    max_instance_request_concurrency = var.container_concurrency

    scaling {
      min_instance_count = var.min_instance_count
      max_instance_count = var.max_instance_count
    }

    containers {
      image = var.image

      ports {
        container_port = var.container_port
      }

      resources {
        limits = {
          cpu    = var.cpu
          memory = var.memory
        }

        cpu_idle          = true
        startup_cpu_boost = true
      }

      dynamic "env" {
        for_each = local.filtered_runtime_env

        content {
          name  = env.key
          value = env.value
        }
      }

      dynamic "env" {
        for_each = var.secret_env

        content {
          name = env.key

          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime[env.value].secret_id
              version = "latest"
            }
          }
        }
      }
    }
  }

  depends_on = [
    google_project_service.required["run.googleapis.com"],
    google_secret_manager_secret_iam_member.api_secret_access,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "public_invoker" {
  count = var.allow_unauthenticated ? 1 : 0

  project  = var.project_id
  location = google_cloud_run_v2_service.api.location
  name     = google_cloud_run_v2_service.api.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}

variable "project_id" {
  description = "Google Cloud project ID where Propagate API resources are deployed."
  type        = string
}

variable "region" {
  description = "Google Cloud region for Cloud Run and Artifact Registry."
  type        = string
  default     = "us-central1"
}

variable "environment" {
  description = "Deployment environment label and PROPAGATE_API_ENV value."
  type        = string
  default     = "dev"

  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "environment must be one of dev, staging, or prod."
  }
}

variable "service_name" {
  description = "Cloud Run service name."
  type        = string
  default     = "propagate-api"
}

variable "artifact_registry_repository_id" {
  description = "Artifact Registry Docker repository ID for API images."
  type        = string
  default     = "propagate"
}

variable "image_tag" {
  description = "Tag of the container image in Artifact Registry to deploy. Used to construct the default image URL."
  type        = string
  default     = "latest"
}

variable "image" {
  description = "Fully qualified container image to deploy to Cloud Run. Defaults to the image built from project_id, region, artifact_registry_repository_id, service_name, and image_tag."
  type        = string
  default     = null
}

variable "container_port" {
  description = "Container port exposed by the API."
  type        = number
  default     = 8080
}

variable "cpu" {
  description = "Cloud Run container CPU limit."
  type        = string
  default     = "1"
}

variable "memory" {
  description = "Cloud Run container memory limit."
  type        = string
  default     = "512Mi"
}

variable "min_instance_count" {
  description = "Minimum Cloud Run instances. Keep at zero for low-cost MVP environments."
  type        = number
  default     = 0
}

variable "max_instance_count" {
  description = "Maximum Cloud Run instances to protect database connection limits and spend."
  type        = number
  default     = 3
}

variable "container_concurrency" {
  description = "Maximum concurrent requests per Cloud Run instance."
  type        = number
  default     = 40
}

variable "request_timeout_seconds" {
  description = "Cloud Run request timeout in seconds."
  type        = number
  default     = 30
}

variable "ingress" {
  description = "Cloud Run ingress setting."
  type        = string
  default     = "INGRESS_TRAFFIC_ALL"

  validation {
    condition = contains([
      "INGRESS_TRAFFIC_ALL",
      "INGRESS_TRAFFIC_INTERNAL_ONLY",
      "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER",
    ], var.ingress)
    error_message = "ingress must be a valid Cloud Run v2 ingress enum."
  }
}

variable "allow_unauthenticated" {
  description = "Whether to grant allUsers roles/run.invoker. Public API routes still enforce Propagate signatures."
  type        = bool
  default     = true
}

variable "deletion_protection" {
  description = "Enable deletion protection on Cloud Run service."
  type        = bool
  default     = false
}

variable "api_version" {
  description = "PROPAGATE_API_VERSION runtime value."
  type        = string
  default     = "0.1.0-dev"
}

variable "min_cli_version" {
  description = "PROPAGATE_MIN_CLI_VERSION runtime value."
  type        = string
  default     = "0.1.0-dev"
}

variable "recommended_cli_version" {
  description = "Optional PROPAGATE_RECOMMENDED_CLI_VERSION runtime value."
  type        = string
  default     = ""
}

variable "request_skew_seconds" {
  description = "Allowed request signing clock skew in seconds."
  type        = number
  default     = 300
}

variable "max_body_bytes" {
  description = "Maximum API request body bytes."
  type        = number
  default     = 4194304
}

variable "log_level" {
  description = "PROPAGATE_LOG_LEVEL runtime value."
  type        = string
  default     = "info"
}

variable "secret_ids" {
  description = "Secret Manager secret IDs to create for API runtime config. Values are populated outside Terraform."
  type        = set(string)
  default = [
    "propagate-database-url",
    "propagate-relay-private-key",
  ]
}

variable "secret_env" {
  description = "Map of Cloud Run env var names to Secret Manager secret IDs."
  type        = map(string)
  default = {
    PROPAGATE_DATABASE_URL = "propagate-database-url"
    PROPAGATE_RELAY_PRIVATE_KEY = "propagate-relay-private-key"
  }
}

variable "labels" {
  description = "Additional labels to apply to supported resources."
  type        = map(string)
  default     = {}
}


terraform {
  required_version = ">= 1.6.0"

  cloud {
    organization = "propagateCLI"
    workspaces {
      name = "propagate-cli"
    }
  }

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

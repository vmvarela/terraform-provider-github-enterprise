terraform {
  required_providers {
    github-enterprise = {
      source = "vmvarela/github-enterprise"
    }
  }
}

provider "github-enterprise" {}

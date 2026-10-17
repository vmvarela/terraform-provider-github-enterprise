terraform {
  required_providers {
    githubenterprise = {
      source = "vmvarela/github-enterprise"
    }
  }
}

provider "githubenterprise" {}

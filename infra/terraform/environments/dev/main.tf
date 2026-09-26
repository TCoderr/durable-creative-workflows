terraform {
  required_version = ">= 1.13, < 2.0"
  backend "azurerm" {}
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.6"
    }
  }
}
provider "azurerm" {
  subscription_id     = var.subscription_id
  tenant_id           = var.tenant_id
  storage_use_azuread = true
  features {
    key_vault {
      purge_soft_delete_on_destroy = false
    }
    resource_group {
      prevent_deletion_if_contains_resources = true
    }
  }
}
module "platform" {
  source                        = "../../modules/platform"
  environment                   = "dev"
  name                          = var.name
  location                      = var.location
  tenant_id                     = var.tenant_id
  aks_admin_group_object_ids    = var.aks_admin_group_object_ids
  postgres_admin_object_id      = var.postgres_admin_object_id
  postgres_admin_principal_name = var.postgres_admin_principal_name
  kubernetes_version            = var.kubernetes_version
  temporal_cloud_address        = var.temporal_cloud_address
}
variable "subscription_id" { type = string }
variable "tenant_id" { type = string }
variable "name" { type = string }
variable "location" { type = string }
variable "aks_admin_group_object_ids" { type = list(string) }
variable "postgres_admin_object_id" { type = string }
variable "postgres_admin_principal_name" { type = string }
variable "kubernetes_version" { type = string }
variable "temporal_cloud_address" { type = string }
output "platform" { value = module.platform }

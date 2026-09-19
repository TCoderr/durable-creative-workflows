variable "name" {
  type = string
  validation {
    condition     = can(regex("^[a-z][a-z0-9]{5,15}$", var.name))
    error_message = "Use 6-16 lowercase letters/digits, starting with a letter; names must be globally unique."
  }
}
variable "environment" {
  type = string
  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "Environment must be dev, staging or prod."
  }
}
variable "location" { type = string }
variable "tenant_id" { type = string }
variable "aks_admin_group_object_ids" { type = list(string) }
variable "postgres_admin_object_id" { type = string }
variable "postgres_admin_principal_name" { type = string }
variable "kubernetes_version" {
  type        = string
  description = "Explicit version supported in the selected Azure region; verify before planning."
}
variable "node_vm_size" {
  type    = string
  default = "Standard_D4s_v5"
}
variable "postgres_sku" {
  type    = string
  default = "GP_Standard_D2ds_v5"
}
variable "temporal_cloud_address" {
  type        = string
  description = "Provisioned Temporal Cloud endpoint; this module does not provision or claim a Cloud namespace."
}
variable "tags" {
  type    = map(string)
  default = {}
}

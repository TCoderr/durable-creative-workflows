locals {
  prefix = "${var.name}-${var.environment}"
  tags   = merge(var.tags, { application = "velin", environment = var.environment, managed_by = "terraform" })
}

resource "azurerm_resource_group" "platform" {
  name     = "${local.prefix}-rg"
  location = var.location
  tags     = local.tags
}
resource "azurerm_virtual_network" "platform" {
  name                = "${local.prefix}-vnet"
  location            = var.location
  resource_group_name = azurerm_resource_group.platform.name
  address_space       = ["10.40.0.0/16"]
  tags                = local.tags
}
resource "azurerm_subnet" "aks" {
  name                 = "aks"
  resource_group_name  = azurerm_resource_group.platform.name
  virtual_network_name = azurerm_virtual_network.platform.name
  address_prefixes     = ["10.40.0.0/20"]
}
resource "azurerm_subnet" "postgres" {
  name                 = "postgres"
  resource_group_name  = azurerm_resource_group.platform.name
  virtual_network_name = azurerm_virtual_network.platform.name
  address_prefixes     = ["10.40.16.0/24"]
  delegation {
    name = "postgres"
    service_delegation {
      name    = "Microsoft.DBforPostgreSQL/flexibleServers"
      actions = ["Microsoft.Network/virtualNetworks/subnets/join/action"]
    }
  }
}
resource "azurerm_subnet" "endpoints" {
  name                 = "private-endpoints"
  resource_group_name  = azurerm_resource_group.platform.name
  virtual_network_name = azurerm_virtual_network.platform.name
  address_prefixes     = ["10.40.17.0/24"]
}
resource "azurerm_log_analytics_workspace" "platform" {
  name                = "${local.prefix}-logs"
  location            = var.location
  resource_group_name = azurerm_resource_group.platform.name
  sku                 = "PerGB2018"
  retention_in_days   = 30
  daily_quota_gb      = 1
  tags                = local.tags
}
resource "azurerm_application_insights" "platform" {
  name                = "${local.prefix}-insights"
  location            = var.location
  resource_group_name = azurerm_resource_group.platform.name
  workspace_id        = azurerm_log_analytics_workspace.platform.id
  application_type    = "other"
  sampling_percentage = 20
  tags                = local.tags
}
resource "azurerm_user_assigned_identity" "cluster" {
  name                = "${local.prefix}-cluster"
  location            = var.location
  resource_group_name = azurerm_resource_group.platform.name
  tags                = local.tags
}
resource "azurerm_role_assignment" "cluster_network" {
  scope                = azurerm_virtual_network.platform.id
  role_definition_name = "Network Contributor"
  principal_id         = azurerm_user_assigned_identity.cluster.principal_id
}
resource "azurerm_container_registry" "platform" {
  name                          = "${var.name}${var.environment}acr"
  location                      = var.location
  resource_group_name           = azurerm_resource_group.platform.name
  sku                           = "Premium"
  admin_enabled                 = false
  public_network_access_enabled = false
  anonymous_pull_enabled        = false
  tags                          = local.tags
}
resource "azurerm_kubernetes_cluster" "platform" {
  name                      = "${local.prefix}-aks"
  location                  = var.location
  resource_group_name       = azurerm_resource_group.platform.name
  dns_prefix                = local.prefix
  kubernetes_version        = var.kubernetes_version
  private_cluster_enabled   = true
  local_account_disabled    = true
  oidc_issuer_enabled       = true
  workload_identity_enabled = true
  sku_tier                  = var.environment == "prod" ? "Standard" : "Free"
  default_node_pool {
    name                         = "system"
    vm_size                      = var.node_vm_size
    auto_scaling_enabled         = true
    min_count                    = var.environment == "prod" ? 3 : 2
    max_count                    = 5
    vnet_subnet_id               = azurerm_subnet.aks.id
    only_critical_addons_enabled = false
    os_disk_type                 = "Ephemeral"
    temporary_name_for_rotation  = "systemtemp"
    upgrade_settings {
      max_surge = "33%"
    }
  }
  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.cluster.id]
  }
  azure_active_directory_role_based_access_control {
    tenant_id              = var.tenant_id
    azure_rbac_enabled     = true
    admin_group_object_ids = var.aks_admin_group_object_ids
  }
  network_profile {
    network_plugin      = "azure"
    network_plugin_mode = "overlay"
    network_policy      = "cilium"
    network_data_plane  = "cilium"
    service_cidr        = "10.41.0.0/16"
    dns_service_ip      = "10.41.0.10"
    pod_cidr            = "10.42.0.0/16"
    outbound_type       = "loadBalancer"
  }
  oms_agent {
    log_analytics_workspace_id      = azurerm_log_analytics_workspace.platform.id
    msi_auth_for_monitoring_enabled = true
  }
  key_vault_secrets_provider {
    secret_rotation_enabled = true
  }
  tags       = local.tags
  depends_on = [azurerm_role_assignment.cluster_network]
}
resource "azurerm_role_assignment" "acr_pull" {
  scope                = azurerm_container_registry.platform.id
  role_definition_name = "AcrPull"
  principal_id         = azurerm_kubernetes_cluster.platform.kubelet_identity[0].object_id
}
resource "azurerm_storage_account" "artifacts" {
  name                            = "${var.name}${var.environment}art"
  location                        = var.location
  resource_group_name             = azurerm_resource_group.platform.name
  account_tier                    = "Standard"
  account_replication_type        = var.environment == "prod" ? "ZRS" : "LRS"
  min_tls_version                 = "TLS1_2"
  public_network_access_enabled   = false
  allow_nested_items_to_be_public = false
  shared_access_key_enabled       = false
  default_to_oauth_authentication = true
  blob_properties {
    versioning_enabled = true
    delete_retention_policy { days = 30 }
    container_delete_retention_policy { days = 30 }
  }
  tags = local.tags
}
resource "azurerm_storage_container" "artifacts" {
  name                  = "artifacts"
  storage_account_id    = azurerm_storage_account.artifacts.id
  container_access_type = "private"
}
resource "azurerm_key_vault" "platform" {
  name                          = "${local.prefix}-kv"
  location                      = var.location
  resource_group_name           = azurerm_resource_group.platform.name
  tenant_id                     = var.tenant_id
  sku_name                      = "standard"
  rbac_authorization_enabled    = true
  purge_protection_enabled      = true
  soft_delete_retention_days    = 90
  public_network_access_enabled = false
  tags                          = local.tags
}
resource "azurerm_private_dns_zone" "zones" {
  for_each = toset([
    "privatelink.blob.core.windows.net",
    "privatelink.vaultcore.azure.net",
    "privatelink.azurecr.io",
    "velin.postgres.database.azure.com"
  ])
  name                = each.key
  resource_group_name = azurerm_resource_group.platform.name
  tags                = local.tags
}
resource "azurerm_private_dns_zone_virtual_network_link" "links" {
  for_each              = azurerm_private_dns_zone.zones
  name                  = "${local.prefix}-link"
  resource_group_name   = azurerm_resource_group.platform.name
  private_dns_zone_name = each.value.name
  virtual_network_id    = azurerm_virtual_network.platform.id
  registration_enabled  = false
}
resource "azurerm_private_endpoint" "services" {
  for_each = {
    blob  = { id = azurerm_storage_account.artifacts.id, subresource = "blob", dns = "privatelink.blob.core.windows.net" }
    vault = { id = azurerm_key_vault.platform.id, subresource = "vault", dns = "privatelink.vaultcore.azure.net" }
    acr   = { id = azurerm_container_registry.platform.id, subresource = "registry", dns = "privatelink.azurecr.io" }
  }
  name                = "${local.prefix}-${each.key}-endpoint"
  location            = var.location
  resource_group_name = azurerm_resource_group.platform.name
  subnet_id           = azurerm_subnet.endpoints.id
  private_service_connection {
    name                           = each.key
    private_connection_resource_id = each.value.id
    subresource_names              = [each.value.subresource]
    is_manual_connection           = false
  }
  private_dns_zone_group {
    name                 = each.key
    private_dns_zone_ids = [azurerm_private_dns_zone.zones[each.value.dns].id]
  }
  tags = local.tags
}
resource "azurerm_postgresql_flexible_server" "domain" {
  name                          = "${local.prefix}-pg"
  location                      = var.location
  resource_group_name           = azurerm_resource_group.platform.name
  version                       = "17"
  sku_name                      = var.postgres_sku
  storage_mb                    = 32768
  backup_retention_days         = 14
  geo_redundant_backup_enabled  = var.environment == "prod"
  public_network_access_enabled = false
  delegated_subnet_id           = azurerm_subnet.postgres.id
  private_dns_zone_id           = azurerm_private_dns_zone.zones["velin.postgres.database.azure.com"].id
  authentication {
    active_directory_auth_enabled = true
    password_auth_enabled         = false
    tenant_id                     = var.tenant_id
  }
  dynamic "high_availability" {
    for_each = var.environment == "prod" ? [1] : []
    content { mode = "ZoneRedundant" }
  }
  tags       = local.tags
  depends_on = [azurerm_private_dns_zone_virtual_network_link.links]
  lifecycle { prevent_destroy = true }
}
resource "azurerm_postgresql_flexible_server_active_directory_administrator" "admin" {
  server_name         = azurerm_postgresql_flexible_server.domain.name
  resource_group_name = azurerm_resource_group.platform.name
  tenant_id           = var.tenant_id
  object_id           = var.postgres_admin_object_id
  principal_name      = var.postgres_admin_principal_name
  principal_type      = "Group"
}
resource "azurerm_postgresql_flexible_server_database" "domain" {
  name      = "velin"
  server_id = azurerm_postgresql_flexible_server.domain.id
  collation = "en_US.utf8"
  charset   = "UTF8"
  lifecycle { prevent_destroy = true }
}
resource "azurerm_user_assigned_identity" "workload" {
  for_each            = toset(["api", "worker", "ai"])
  name                = "${local.prefix}-${each.key}"
  location            = var.location
  resource_group_name = azurerm_resource_group.platform.name
  tags                = local.tags
}
resource "azurerm_federated_identity_credential" "workload" {
  for_each            = azurerm_user_assigned_identity.workload
  name                = "${local.prefix}-${each.key}-federation"
  resource_group_name = azurerm_resource_group.platform.name
  parent_id           = each.value.id
  audience            = ["api://AzureADTokenExchange"]
  issuer              = azurerm_kubernetes_cluster.platform.oidc_issuer_url
  subject             = "system:serviceaccount:velin-${var.environment}:velin-${each.key}"
}
resource "azurerm_role_assignment" "artifact_writer" {
  scope                = azurerm_storage_account.artifacts.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = azurerm_user_assigned_identity.workload["worker"].principal_id
}
resource "azurerm_role_assignment" "artifact_reader" {
  scope                = azurerm_storage_account.artifacts.id
  role_definition_name = "Storage Blob Data Reader"
  principal_id         = azurerm_user_assigned_identity.workload["api"].principal_id
}

# Secrets themselves are provisioned out of band; Terraform state contains no values.
resource "azurerm_role_assignment" "secret_reader" {
  for_each             = azurerm_user_assigned_identity.workload
  scope                = azurerm_key_vault.platform.id
  role_definition_name = "Key Vault Secrets User"
  principal_id         = each.value.principal_id
}

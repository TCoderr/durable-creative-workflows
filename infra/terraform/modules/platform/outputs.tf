output "resource_group" { value = azurerm_resource_group.platform.name }
output "aks_name" { value = azurerm_kubernetes_cluster.platform.name }
output "acr_login_server" { value = azurerm_container_registry.platform.login_server }
output "postgres_fqdn" { value = azurerm_postgresql_flexible_server.domain.fqdn }
output "artifact_blob_endpoint" { value = azurerm_storage_account.artifacts.primary_blob_endpoint }
output "key_vault_uri" { value = azurerm_key_vault.platform.vault_uri }
output "workload_client_ids" { value = { for name, identity in azurerm_user_assigned_identity.workload : name => identity.client_id } }
output "temporal_cloud_address" { value = var.temporal_cloud_address }

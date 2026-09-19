# ADR 0003: Azure AKS target with Temporal Cloud; deployment unexecuted

Status: accepted production target.

AKS with workload identity, managed PostgreSQL, Blob Storage, ACR and Key Vault provide persistence and identity behind private networking. Temporal Cloud avoids owning persistence, upgrades and cluster recovery for the orchestration engine; namespace credentials, retention and worker versioning remain configuration work. Local Compose uses the Temporal development server with a persisted SQLite volume for verification only.

Terraform validation and local topology execution are separate evidence from cloud deployment. Costs are nontrivial and the target is a production design, not an always-on demo. A smaller container platform could serve the same services at lower cost at the price of changing the accepted target.

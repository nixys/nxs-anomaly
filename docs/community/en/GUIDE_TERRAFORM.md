# Manage resources with Terraform

## Before you begin

Install Terraform on the runner. Obtain the HTTPS base URL of the API and a scoped API key. A viewer can read the example below; managing teams or chains requires write permissions. [Installation options](INSTALLATION.md). [Install the CLI](https://developer.hashicorp.com/terraform/install).

## Connect and configure

1. Save the configuration below in main.tf.
2. Set `NXS_ANOMALY_URL` and `NXS_ANOMALY_API_KEY` in the runner environment.
3. Run terraform init, terraform validate and terraform plan.
4. Store the provider lockfile and protect access to state.

```hcl
terraform {
  required_providers {
    anomaly = { source = "nixys/nxs-anomaly" }
  }
}
provider "anomaly" {}
data "anomaly_teams" "all" {}
output "team_names" { value = data.anomaly_teams.all.teams[*].name }
```

## Verify and troubleshoot

Compare the plan's team names with those visible to the API identity. For 403 verify role and team scope. Import existing resources before managing them and review the plan before each apply. [Provider resources](https://github.com/nixys/terraform-provider-nxs-anomaly).

[Deployment reference](DEPLOY.md) · [Integration catalogue](INTEGRATIONS.md)

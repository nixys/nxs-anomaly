terraform {
  required_providers {
    anomaly = {
      source  = "nixys/nxs-anomaly"
      version = "= 0.1.1"
    }
  }
}
provider "anomaly" {}
resource "anomaly_user" "example" {
  name     = "Terraform example recipient"
  username = "terraform.example"
}

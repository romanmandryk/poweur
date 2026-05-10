# poweur — Infrastructure

Hetzner Cloud infrastructure for the Poweur ID relay, defined in Terraform.

## What this provisions

- Hetzner Cloud server(s) running the Go relay
- Hetzner Load Balancer in front of relay instance(s)
- DNS records pointing to the load balancer (via Hetzner DNS)
- Wildcard TLS certificate (`*.poweur.net`) via Terraform ACME + Let's Encrypt (DNS-01 challenge)
- Firewall rules restricting direct server access

## Prerequisites

- [Terraform](https://developer.hashicorp.com/terraform) ≥ 1.6
- A Hetzner Cloud account and API token (`HCLOUD_TOKEN`)
- A Hetzner DNS API token (`HETZNER_DNS_TOKEN`) — used for both DNS record management and ACME DNS-01 challenge
- A domain whose DNS is managed in Hetzner DNS

## Usage

```sh
export HCLOUD_TOKEN=...
export HETZNER_DNS_TOKEN=...

terraform init
terraform plan
terraform apply
```

## TLS Certificate

The wildcard cert (`*.poweur.net`) is provisioned by the Terraform ACME provider using a DNS-01 challenge against the Hetzner DNS API. DNS-01 is required — HTTP-01 does not support wildcard certificates.

**Renewal:** Re-run `terraform apply` (e.g., via a scheduled CI job) — the ACME provider checks expiry and renews automatically when within the renewal window.

**Fallback:** If you prefer on-server renewal, install `certbot` or `acme.sh` with the Hetzner DNS plugin and set up a cron job. In this case, disable the ACME resource in `main.tf` to avoid conflicts.

## Certificate storage

By default, the certificate and private key are written to the relay server at `/etc/poweur/tls/` during provisioning. Alternatively, store them in Hetzner Object Storage and configure the relay to fetch them at startup — see `variables.tf` for the `cert_storage` option.

## Structure

```
apps/infra/
  main.tf         # Core resource definitions
  variables.tf    # Input variables
  outputs.tf      # Useful outputs (LB IP, cert expiry, etc.)
  README.md       # This file
```

---
id: tls
sidebar_position: 2
title: TLS Configuration
---

# TLS Configuration

All relay-to-relay and client-to-relay communication must use HTTPS. This page describes the TLS certificate strategy, provisioning approach, and renewal automation for Poweur ID relay deployments.

## Wildcard Certificate Strategy

A relay hosting identities on a domain (e.g. `poweur.net`) needs a TLS certificate that covers all identity subdomains (`alice.poweur.net`, `bob.poweur.net`, etc.). The correct strategy is a **single wildcard certificate** for `*.poweur.net`.

Why a wildcard:

- **One cert covers all identities.** Adding a new identity (`carol.poweur.net`) requires no certificate change.
- **No per-identity provisioning.** There is no need to run ACME HTTP-01 or DNS-01 challenges per identity.
- **Simpler renewal.** A single cert to track, renew, and deploy.

The one-level wildcard limitation (`*.poweur.net` covers `alice.poweur.net` but not `deep.alice.poweur.net`) is not a concern because the protocol uses only first-level subdomains for identities.

## Challenge Type: DNS-01 (Required)

The ACME DNS-01 challenge is **required** for wildcard certificates. HTTP-01 cannot prove control over `*.poweur.net`.

DNS-01 works by having the certificate provisioner create a temporary `TXT` record (`_acme-challenge.poweur.net`) that Let's Encrypt verifies before issuing the certificate.

The Poweur ID infrastructure (`apps/infra`) automates DNS-01 via the Terraform ACME provider, using the same Hetzner DNS API credentials used for identity record management. No additional provider accounts are needed.

## Provisioning with Terraform ACME

The recommended approach for production deployments is to provision and renew the wildcard certificate using the **Terraform ACME provider** against **Let's Encrypt**.

```hcl
# Simplified excerpt from apps/infra

terraform {
  required_providers {
    acme    = { source = "vancluever/acme",  version = "~> 2.0" }
    hetzner = { source = "hetznercloud/hcloud" }
    dns     = { source = "hashicorp/dns" }
  }
}

provider "acme" {
  server_url = "https://acme-v02.api.letsencrypt.org/directory"
}

resource "tls_private_key" "acme_account" {
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "acme_registration" "relay" {
  account_key_pem = tls_private_key.acme_account.private_key_pem
  email_address   = "ops@poweur.net"
}

resource "acme_certificate" "wildcard" {
  account_key_pem           = acme_registration.relay.account_key_pem
  common_name               = "*.poweur.net"
  subject_alternative_names = ["poweur.net"]

  dns_challenge {
    provider = "hetzner"
    config = {
      HETZNER_API_KEY = var.hetzner_dns_token
    }
  }
}
```

The certificate and private key are written to the Hetzner server via Terraform provisioner, or stored in Hetzner Object Storage for the relay to fetch at startup.

### Renewal

Renewal is handled by re-running `terraform apply`. The ACME provider checks the certificate's expiry and renews automatically when it falls within the renewal window (typically 30 days before expiry).

The recommended approach is to schedule `terraform apply` via a CI job (e.g. a GitHub Actions workflow on a weekly cron schedule). All certificate state is in Terraform — no on-server renewal daemon is needed.

## Fallback: On-Server Renewal with certbot

If Terraform-managed renewal is not suitable, certificates can be managed on the relay server using `certbot` with the Hetzner DNS plugin.

```bash
# Install certbot and Hetzner DNS plugin
pip install certbot certbot-dns-hetzner

# Obtain wildcard certificate
certbot certonly \
  --dns-hetzner \
  --dns-hetzner-credentials ~/.secrets/hetzner-dns.ini \
  -d "*.poweur.net" \
  -d "poweur.net"
```

Where `~/.secrets/hetzner-dns.ini` contains:

```ini
dns_hetzner_api_token = <hetzner-dns-token>
```

Renewal is handled by certbot's built-in renewal timer (`/etc/cron.d/certbot` or `systemd certbot.timer`).

The on-server approach is simpler to set up but splits infrastructure state between Terraform and the server, making it harder to audit and reproduce.

## Load Balancer TLS Termination

For production deployments, TLS is terminated at the **Hetzner Load Balancer** in front of the relay server(s), not at the relay process itself:

1. The provisioned wildcard certificate is uploaded to the Hetzner Load Balancer.
2. The load balancer handles TLS termination and forwards plaintext HTTP to relay instances.
3. Relay instances bind to port 8080 (or as configured) with `TLS_ENABLE=false`.
4. Communication between the load balancer and relay instances is over a private Hetzner network (not exposed to the internet).

This offloads TLS from the relay process and allows the certificate to be updated at the load balancer without touching relay instances.

## TLS Certificate Deployment Options

| Option | Pros | Cons |
|--------|------|------|
| Terraform ACME provider (recommended) | All state in Terraform; automated renewal via CI | Requires Terraform access for renewal |
| certbot on-server | Simple; self-contained | Splits state; requires server-level cron |
| Load balancer managed cert | Delegate renewal to Hetzner | Some providers have cert count limits |

## Related

- [Security Model](/security/model)
- [Relay Configuration](/relay/configuration)
- [Routing](/protocol/routing)

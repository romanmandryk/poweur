terraform {
  required_version = ">= 1.6"

  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.47"
    }
    hetznerdns = {
      source  = "timohirt/hetznerdns"
      version = "~> 2.2"
    }
    acme = {
      source  = "vancluever/acme"
      version = "~> 2.21"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}

# ---------------------------------------------------------------------------
# Providers
# ---------------------------------------------------------------------------

provider "hcloud" {
  token = var.hcloud_token
}

provider "hetznerdns" {
  apitoken = var.hetzner_dns_token
}

provider "acme" {
  server_url = "https://acme-v02.api.letsencrypt.org/directory"
}

# ---------------------------------------------------------------------------
# TLS — wildcard certificate via ACME + Let's Encrypt (DNS-01)
# ---------------------------------------------------------------------------

resource "tls_private_key" "acme_account" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "acme_registration" "relay" {
  account_key_pem = tls_private_key.acme_account.private_key_pem
  email_address   = var.acme_email
}

resource "acme_certificate" "wildcard" {
  account_key_pem = acme_registration.relay.account_key_pem
  common_name     = "*.${var.domain}"

  dns_challenge {
    provider = "hetznerdns"
    config = {
      HETZNER_API_KEY = var.hetzner_dns_token
    }
  }
}

# ---------------------------------------------------------------------------
# Relay server(s)
# ---------------------------------------------------------------------------

resource "hcloud_server" "relay" {
  count       = var.relay_count
  name        = "poweur-relay-${count.index + 1}"
  server_type = var.server_type
  image       = "ubuntu-24.04"
  location    = var.location

  labels = {
    app = "poweur-relay"
  }

  # TODO: add user_data cloud-init script to install relay binary and systemd unit
}

# ---------------------------------------------------------------------------
# Load Balancer
# ---------------------------------------------------------------------------

resource "hcloud_load_balancer" "relay" {
  name               = "poweur-lb"
  load_balancer_type = var.lb_type
  location           = var.location

  labels = {
    app = "poweur-relay"
  }
}

resource "hcloud_load_balancer_target" "relay" {
  count            = var.relay_count
  type             = "server"
  load_balancer_id = hcloud_load_balancer.relay.id
  server_id        = hcloud_server.relay[count.index].id
}

resource "hcloud_load_balancer_service" "https" {
  load_balancer_id = hcloud_load_balancer.relay.id
  protocol         = "https"
  listen_port      = 443
  destination_port = 8080

  http {
    certificates = [] # TODO: reference Hetzner managed cert or upload wildcard cert
    sticky_sessions = false
  }

  health_check {
    protocol = "http"
    port     = 8080
    interval = 15
    timeout  = 10
    retries  = 3
    http {
      path         = "/health"
      status_codes = ["200"]
    }
  }
}

# ---------------------------------------------------------------------------
# DNS — relay hostname pointing to LB
# ---------------------------------------------------------------------------

data "hetznerdns_zone" "main" {
  name = var.domain
}

resource "hetznerdns_record" "relay" {
  zone_id = data.hetznerdns_zone.main.id
  name    = "relay"
  value   = hcloud_load_balancer.relay.ipv4
  type    = "A"
  ttl     = 300
}

# ---------------------------------------------------------------------------
# Firewall — allow only LB and operator IPs to reach relay servers directly
# ---------------------------------------------------------------------------

resource "hcloud_firewall" "relay" {
  name = "poweur-relay-fw"

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "8080"
    source_ips = ["0.0.0.0/0", "::/0"] # TODO: restrict to LB IP once known
  }

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = var.operator_ips
  }
}

resource "hcloud_firewall_attachment" "relay" {
  firewall_id = hcloud_firewall.relay.id
  server_ids  = hcloud_server.relay[*].id
}

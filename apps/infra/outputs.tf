output "load_balancer_ipv4" {
  description = "Public IPv4 address of the Hetzner Load Balancer"
  value       = hcloud_load_balancer.relay.ipv4
}

output "relay_server_ips" {
  description = "IPv4 addresses of relay server instances"
  value       = hcloud_server.relay[*].ipv4_address
}

output "certificate_expires" {
  description = "Expiry date of the wildcard TLS certificate"
  value       = acme_certificate.wildcard.certificate_not_after
}

output "relay_hostname" {
  description = "Relay hostname (relay.<domain>)"
  value       = "relay.${var.domain}"
}

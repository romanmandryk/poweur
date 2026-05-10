variable "hcloud_token" {
  description = "Hetzner Cloud API token"
  type        = string
  sensitive   = true
}

variable "hetzner_dns_token" {
  description = "Hetzner DNS API token (used for DNS record management and ACME DNS-01 challenge)"
  type        = string
  sensitive   = true
}

variable "domain" {
  description = "Parent domain for the relay (e.g. poweur.net). Wildcard cert covers *.domain."
  type        = string
}

variable "acme_email" {
  description = "Email address for Let's Encrypt ACME account registration"
  type        = string
}

variable "relay_count" {
  description = "Number of relay server instances to provision"
  type        = number
  default     = 1
}

variable "server_type" {
  description = "Hetzner Cloud server type"
  type        = string
  default     = "cpx11"
}

variable "lb_type" {
  description = "Hetzner Load Balancer type"
  type        = string
  default     = "lb11"
}

variable "location" {
  description = "Hetzner Cloud location (e.g. fsn1, nbg1, hel1)"
  type        = string
  default     = "nbg1"
}

variable "operator_ips" {
  description = "CIDR ranges permitted SSH access to relay servers"
  type        = list(string)
  default     = []
}

variable "cert_storage" {
  description = "Where to store the TLS certificate: 'server' (write to /etc/poweur/tls/) or 'object-storage'"
  type        = string
  default     = "server"
  validation {
    condition     = contains(["server", "object-storage"], var.cert_storage)
    error_message = "cert_storage must be 'server' or 'object-storage'"
  }
}

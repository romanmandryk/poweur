#!/usr/bin/env bash
# Bootstrap a fresh Ubuntu 24.04 Hetzner box for the poweur production stack.
# Run as root from inside the repo: sudo bash deploy/server-bootstrap.sh
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
REPO_URL="${REPO_URL:-git@github.com:romanmandryk/poweur.git}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
info()  { echo -e "${GREEN}  ✓${NC} $*"; }
warn()  { echo -e "${YELLOW}  !${NC} $*"; }
step()  { echo -e "\n${CYAN}━━━ $* ━━━${NC}"; }
banner(){ echo -e "\n${GREEN}$*${NC}"; }

[[ $EUID -ne 0 ]] && { echo -e "${RED}Run as root: sudo bash $0${NC}"; exit 1; }

# ── 1. Packages ───────────────────────────────────────────────────────────────
step "1/8  System packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq \
  git curl ca-certificates gnupg lsb-release rsync ufw jq openssl

if ! command -v docker &>/dev/null; then
  info "Installing Docker CE..."
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
    | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  echo \
    "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
    https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq \
    docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
  info "Docker installed"
else
  info "Docker already present"
fi

# ── 2. Users ──────────────────────────────────────────────────────────────────
step "2/8  Linux users"
for user in infra poweur; do
  if ! id "$user" &>/dev/null; then
    useradd -m -s /bin/bash "$user"
    info "Created user: $user"
  else
    info "User already exists: $user"
  fi
  usermod -aG docker "$user"
done

# ── 3. Directories ────────────────────────────────────────────────────────────
step "3/8  Directories"
install -d -m 750 -o infra  -g infra  /opt/infra
install -d -m 750 -o poweur -g poweur /opt/apps
install -d -m 750 -o poweur -g poweur /opt/apps/poweur
info "Directories ready"

# ── 4. Infra configuration ────────────────────────────────────────────────────
step "4/8  Infra configuration"
rsync -a --chown=infra:infra "$REPO_ROOT/deploy/infra/" /opt/infra/
chmod 750 /opt/infra
info "Infra config synced to /opt/infra"

if [[ ! -f /opt/infra/.env ]]; then
  cat > /opt/infra/.env <<'EOF'
POSTGRES_USER=postgres
POSTGRES_PASSWORD=CHANGE_ME
GRAFANA_PASSWORD=CHANGE_ME
GRAFANA_DB_PASSWORD=CHANGE_ME
EOF
  chown infra:infra /opt/infra/.env
  chmod 600 /opt/infra/.env
  warn "Created /opt/infra/.env — EDIT PASSWORDS before starting services"
else
  info ".env already exists, skipping"
fi

# ── 5. SSH keys ───────────────────────────────────────────────────────────────
step "5/8  SSH keys"
install -d -m 700 -o poweur -g poweur /home/poweur/.ssh

# Key A — server's git identity (used by `git pull` on the server)
GIT_KEY=/home/poweur/.ssh/github_deploy
if [[ ! -f $GIT_KEY ]]; then
  sudo -u poweur ssh-keygen -t ed25519 -f "$GIT_KEY" -N "" -C "poweur-server" -q
  cat > /home/poweur/.ssh/config <<EOF
Host github.com
  IdentityFile ~/.ssh/github_deploy
  StrictHostKeyChecking accept-new
EOF
  chown poweur:poweur /home/poweur/.ssh/config
  chmod 600 /home/poweur/.ssh/config
  info "Generated git identity key: $GIT_KEY"
else
  info "Git identity key already exists"
fi
GIT_PUBKEY=$(cat "${GIT_KEY}.pub")

# Key B — GitHub Actions → server SSH (private key printed once, then deleted)
ACTIONS_KEY=$(mktemp /tmp/actions_deploy.XXXXXX)
ssh-keygen -t ed25519 -f "$ACTIONS_KEY" -N "" -C "github-actions" -q
touch /home/poweur/.ssh/authorized_keys
if ! grep -qF "$(cat ${ACTIONS_KEY}.pub)" /home/poweur/.ssh/authorized_keys 2>/dev/null; then
  cat "${ACTIONS_KEY}.pub" >> /home/poweur/.ssh/authorized_keys
fi
chown poweur:poweur /home/poweur/.ssh/authorized_keys
chmod 600 /home/poweur/.ssh/authorized_keys
ACTIONS_PRIVKEY=$(cat "$ACTIONS_KEY")
rm -f "$ACTIONS_KEY" "${ACTIONS_KEY}.pub"
info "Generated GitHub Actions deploy key (printed below)"

# ── 6. Clone app repo ─────────────────────────────────────────────────────────
step "6/8  Clone app repo"
if [[ ! -d /opt/apps/poweur/.git ]]; then
  info "Attempting git clone (requires deploy key to be added to GitHub first)..."
  if sudo -u poweur git clone "$REPO_URL" /opt/apps/poweur 2>/dev/null; then
    info "Repo cloned to /opt/apps/poweur"
  else
    warn "Clone failed — complete manually after adding deploy key to GitHub:"
    warn "  sudo -u poweur git clone $REPO_URL /opt/apps/poweur"
  fi
else
  info "Repo already present at /opt/apps/poweur"
fi

if [[ ! -f /opt/apps/poweur/apps/api/.env.prod ]]; then
  cat > /opt/apps/poweur/apps/api/.env.prod <<'EOF'
LISTEN_ADDR=:8080
RELAY_ADDRESS=relay.poweur.net
RELAY_SCHEME=https
DNS_PROXY_MODE=auto
EOF
  chown poweur:poweur /opt/apps/poweur/apps/api/.env.prod
  chmod 600 /opt/apps/poweur/apps/api/.env.prod
  warn "Created apps/api/.env.prod — review and add any additional env vars"
fi

# ── 7. Docker network ─────────────────────────────────────────────────────────
step "7/8  Docker network"
if ! docker network inspect infra_net &>/dev/null; then
  docker network create infra_net
  info "Created Docker network: infra_net"
else
  info "Docker network infra_net already exists"
fi

# ── 8. Boot policy ────────────────────────────────────────────────────────────
step "8/8  Boot policy"
# Releases are `docker compose up` from GitHub Actions. Docker's
# restart: unless-stopped brings containers back after reboot. Do not install
# the old foreground compose systemd units; they fight Compose.
systemctl enable docker.service
systemctl disable --now poweur-app.service poweur-infra.service 2>/dev/null || true
info "Docker enabled on boot; legacy compose units left disabled"

# ── Firewall ──────────────────────────────────────────────────────────────────
ufw allow 22/tcp   comment "SSH"   >/dev/null
ufw allow 80/tcp   comment "HTTP"  >/dev/null
ufw allow 443/tcp  comment "HTTPS" >/dev/null
ufw allow 443/udp  comment "HTTP/3" >/dev/null
ufw --force enable >/dev/null
info "UFW firewall configured (22/80/443)"

# ── Summary ───────────────────────────────────────────────────────────────────
banner "
╔══════════════════════════════════════════════════════════════╗
║                    Bootstrap complete                        ║
╚══════════════════════════════════════════════════════════════╝"

echo -e "\n${YELLOW}ACTION 1 of 3 — Add this as a GitHub Deploy Key (read-only)${NC}"
echo -e "${CYAN}Repo → Settings → Deploy keys → Add deploy key${NC}\n"
echo "$GIT_PUBKEY"

echo -e "\n${YELLOW}ACTION 2 of 3 — Add this private key as a GitHub Actions secret${NC}"
echo -e "${CYAN}Repo → Settings → Secrets and variables → Actions → New secret${NC}"
echo -e "${CYAN}Name: DEPLOY_SSH_KEY${NC}\n"
echo "$ACTIONS_PRIVKEY"

echo -e "\n${YELLOW}ACTION 3 of 3 — Add DEPLOY_HOST secret${NC}"
echo -e "${CYAN}Name: DEPLOY_HOST   Value: $(curl -4 -s ifconfig.me 2>/dev/null || echo '<your-server-ip>')${NC}"

echo -e "\n${YELLOW}Finish with deploy/README.md:${NC}"
echo -e "  ${CYAN}sudo bash deploy/setup-observability.sh --file .observability.env --import-env /opt/infra/.env${NC}"
echo -e "  ${CYAN}nano /opt/apps/poweur/apps/api/.env.prod${NC}   (app config)"
echo -e "  Then push to master (Deploy runs on that push)."
echo ""

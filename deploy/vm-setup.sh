#!/usr/bin/env bash
# Run once on a fresh GCP e2-micro (Debian 12) to bootstrap the VM.
# Usage: bash vm-setup.sh
set -euo pipefail

#──── Configuration ────────────────────────────────────────────────────────────
GCP_PROJECT="${GCP_PROJECT:?set GCP_PROJECT}"
GCS_BUCKET="${GCS_BUCKET:?set GCS_BUCKET}"
AGENTCOCKPIT_SECRET="${AGENTCOCKPIT_SECRET:?set AGENTCOCKPIT_SECRET}"
DEPLOY_USER="${DEPLOY_USER:-deploy}"
APP_DIR=/opt/agentcockpit
ENV_FILE=/etc/agentcockpit.env
IMAGE_REPO="us-docker.pkg.dev/${GCP_PROJECT}/agentcockpit/server"
#───────────────────────────────────────────────────────────────────────────────

echo "==> Installing system packages"
apt-get update -q
apt-get install -y -q docker.io docker-compose-plugin curl ufw

systemctl enable --now docker

echo "==> Creating deploy user"
if ! id "${DEPLOY_USER}" &>/dev/null; then
  useradd -m -s /bin/bash "${DEPLOY_USER}"
fi
usermod -aG docker "${DEPLOY_USER}"

# Set up SSH for the deploy user (GitHub Actions will need this key)
DEPLOY_HOME=$(eval echo ~"${DEPLOY_USER}")
mkdir -p "${DEPLOY_HOME}/.ssh"
chmod 700 "${DEPLOY_HOME}/.ssh"
# Paste the PUBLIC key of the deploy SSH key here (the private key goes in GitHub secrets)
# echo "ssh-ed25519 AAAA..." >> "${DEPLOY_HOME}/.ssh/authorized_keys"
chmod 600 "${DEPLOY_HOME}/.ssh/authorized_keys" 2>/dev/null || true
chown -R "${DEPLOY_USER}:${DEPLOY_USER}" "${DEPLOY_HOME}/.ssh"

echo "==> Configuring Application Default Credentials"
# Authenticate gcloud as root so Docker can read ~/.config/gcloud
# The VM service account needs roles/storage.objectAdmin on the GCS bucket
# and roles/artifactregistry.reader on the Artifact Registry repo.
gcloud auth configure-docker us-docker.pkg.dev --quiet

echo "==> Creating app directory"
mkdir -p "${APP_DIR}"
# Copy docker-compose.yml and litestream.yml from this repo
cp "$(dirname "$0")/docker-compose.yml" "${APP_DIR}/"
cp "$(dirname "$0")/litestream.yml"     "${APP_DIR}/"
chown -R "${DEPLOY_USER}:${DEPLOY_USER}" "${APP_DIR}"

echo "==> Writing env file ${ENV_FILE}"
cat > "${ENV_FILE}" <<EOF
GCP_PROJECT=${GCP_PROJECT}
GCS_BUCKET=${GCS_BUCKET}
AGENTCOCKPIT_SECRET=${AGENTCOCKPIT_SECRET}
IMAGE_TAG=latest
EOF
chmod 600 "${ENV_FILE}"

echo "==> Configuring firewall (UFW)"
ufw --force reset
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp    comment "SSH"
ufw allow 80/tcp    comment "HTTP (Cloudflare)"
ufw allow 443/tcp   comment "HTTPS (Cloudflare)"
# Port 7080 is bound to 127.0.0.1 only — NOT opened externally
ufw --force enable

echo "==> Starting services for the first time"
cd "${APP_DIR}"
docker compose --env-file "${ENV_FILE}" pull
docker compose --env-file "${ENV_FILE}" up -d

echo ""
echo "Done! VM is ready."
echo ""
echo "Next steps:"
echo "  1. Point agentcockpit.app DNS (A record) to this VM's IP"
echo "  2. Set up Cloudflare in front for TLS (see docs below)"
echo "  3. Add GitHub repo secrets (see deploy.yml header)"
echo "  4. Push to main to trigger first deploy"
echo ""
echo "Check status:"
echo "  docker compose --env-file ${ENV_FILE} -f ${APP_DIR}/docker-compose.yml ps"
echo "  docker compose --env-file ${ENV_FILE} -f ${APP_DIR}/docker-compose.yml logs -f"

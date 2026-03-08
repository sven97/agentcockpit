#!/usr/bin/env bash
# One-time setup to migrate AgentCockpit from GCE VM to Cloud Run.
# Run this manually once. After this, GitHub Actions handles all deploys.
#
# Prerequisites:
#   gcloud auth login
#   gcloud config set project <GCP_PROJECT>
#
# Usage:
#   GCP_PROJECT=agentcockpit-app \
#   GCS_BUCKET=your-litestream-bucket \
#   AGENTCOCKPIT_SECRET=your-32-char-secret \
#   bash deploy/cloudrun-setup.sh
set -euo pipefail

GCP_PROJECT="${GCP_PROJECT:?set GCP_PROJECT}"
GCS_BUCKET="${GCS_BUCKET:?set GCS_BUCKET}"
AGENTCOCKPIT_SECRET="${AGENTCOCKPIT_SECRET:?set AGENTCOCKPIT_SECRET}"
REGION="us-central1"
SA="cloudrun@${GCP_PROJECT}.iam.gserviceaccount.com"
GITHUB_SA="github-actions@${GCP_PROJECT}.iam.gserviceaccount.com"

echo "==> Enabling required APIs"
gcloud services enable \
  run.googleapis.com \
  secretmanager.googleapis.com \
  --project="${GCP_PROJECT}"

echo "==> Creating Cloud Run service account"
gcloud iam service-accounts create cloudrun \
  --display-name="Cloud Run AgentCockpit" \
  --project="${GCP_PROJECT}" 2>/dev/null || echo "  (already exists)"

echo "==> Granting Artifact Registry read access"
gcloud projects add-iam-policy-binding "${GCP_PROJECT}" \
  --member="serviceAccount:${SA}" \
  --role="roles/artifactregistry.reader" \
  --condition=None

echo "==> Granting GCS bucket access (Litestream)"
gcloud storage buckets add-iam-policy-binding "gs://${GCS_BUCKET}" \
  --member="serviceAccount:${SA}" \
  --role="roles/storage.objectAdmin"

echo "==> Storing AGENTCOCKPIT_SECRET in Secret Manager"
echo -n "${AGENTCOCKPIT_SECRET}" | gcloud secrets create agentcockpit-secret \
  --data-file=- --project="${GCP_PROJECT}" 2>/dev/null || \
echo -n "${AGENTCOCKPIT_SECRET}" | gcloud secrets versions add agentcockpit-secret \
  --data-file=- --project="${GCP_PROJECT}"

echo "==> Granting Cloud Run SA access to the secret"
gcloud secrets add-iam-policy-binding agentcockpit-secret \
  --member="serviceAccount:${SA}" \
  --role="roles/secretmanager.secretAccessor" \
  --project="${GCP_PROJECT}"

echo "==> Granting GitHub Actions SA permission to deploy Cloud Run"
gcloud projects add-iam-policy-binding "${GCP_PROJECT}" \
  --member="serviceAccount:${GITHUB_SA}" \
  --role="roles/run.admin" \
  --condition=None

gcloud iam service-accounts add-iam-policy-binding "${SA}" \
  --member="serviceAccount:${GITHUB_SA}" \
  --role="roles/iam.serviceAccountUser" \
  --project="${GCP_PROJECT}"

echo ""
echo "Done! Next steps:"
echo "  1. Add GCS_BUCKET='${GCS_BUCKET}' to GitHub repo secrets"
echo "  2. Push to main to trigger first Cloud Run deploy"
echo "  3. Get the Cloud Run URL:"
echo "     gcloud run services describe agentcockpit --region=${REGION} --format='value(status.url)'"
echo "  4. In Cloudflare DNS: update agentcockpit.app CNAME to the Cloud Run URL"
echo "     (remove the port, just the hostname)"
echo "  5. Once confirmed working, stop the GCE VM container:"
echo "     gcloud compute ssh agentcockpit-vm --command='sudo docker stop agentcockpit-agentcockpit-1'"

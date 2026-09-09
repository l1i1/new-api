#!/bin/bash
# Bootstrap a New API host instance on CN-SH-SWAS-2 (mainland panel origin).
# Env + image digest + registry creds are pulled live from the ECI scaling
# configuration so the host always matches the ECI release exactly.
# No secrets are printed; the env file is chmod 600 under /opt/newapi-host.
# This container is the fleet MASTER (NODE_TYPE=master, NODE_NAME=new-api-host-sw2);
# ECI instances are slaves managed by deploy.sh's node-identity env injection.
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
export PATH="/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export HOME=/root
ASG_CONFIG_ID="asc-uf641n1j5akwa1p0smug"

if ! command -v docker >/dev/null 2>&1; then
  apt-get update -qq
  apt-get install -y -qq docker.io >/dev/null
fi
systemctl enable --now docker >/dev/null 2>&1 || true

mkdir -p /opt/newapi-host
chmod 700 /opt/newapi-host

CONF_JSON=$(aliyun ess DescribeEciScalingConfigurations \
  --RegionId cn-shanghai --ScalingConfigurationId "$ASG_CONFIG_ID" | tr -d '\r')

# Node identity: this host container is the single stable MASTER (it runs DB
# migrations and system/background tasks). The ECI tier runs as slave with
# NODE_NAME=new-api-ml000, so both instances report distinct identities in the
# panel instance list and tasks never execute twice. Hostname fallbacks (ECI
# container id / SWAS-2 host id) are not stable across recreations.
HOST_NODE_NAME="${HOST_NODE_NAME:-new-api-host-sw2}"
HOST_NODE_TYPE="${HOST_NODE_TYPE:-master}"

echo "$CONF_JSON" | python3 -c '
import json, sys
node_name = sys.argv[1]
node_type = sys.argv[2]
sc = json.load(sys.stdin)["ScalingConfigurations"][0]
c = sc["Containers"][0]
envs = {e["Key"]: e["Value"] for e in c.get("EnvironmentVars", [])}
envs["NODE_NAME"] = node_name
envs["NODE_TYPE"] = node_type
for k in sorted(envs):
    print("%s=%s" % (k, envs[k]))
' "$HOST_NODE_NAME" "$HOST_NODE_TYPE" > /opt/newapi-host/env
chmod 600 /opt/newapi-host/env

DIGEST=$(echo "$CONF_JSON" | python3 -c '
import json, sys
print(json.load(sys.stdin)["ScalingConfigurations"][0]["Containers"][0]["Image"])')

eval "$(echo "$CONF_JSON" | python3 -c '
import json, sys
creds = json.load(sys.stdin)["ScalingConfigurations"][0].get("ImageRegistryCredentials") or []
if creds:
    print("REG_USER=%s" % creds[0]["UserName"])
    print("REG_PASS=%s" % creds[0]["Password"])
')"

if [ -n "${REG_USER:-}" ]; then
  printf '%s' "$REG_PASS" | docker login docker.cnb.cool -u "$REG_USER" --password-stdin >/dev/null
fi

echo "pulling $DIGEST"
docker pull "$DIGEST" >/dev/null 2>&1 || docker pull "$DIGEST"

docker rm -f newapi-host >/dev/null 2>&1 || true
docker run -d --name newapi-host \
  --restart unless-stopped \
  --env-file /opt/newapi-host/env \
  --memory 1536m \
  -p 172.24.63.126:8300:3000 \
  "$DIGEST"

echo "waiting for app readiness..."
# Dependency-aware gate (/health/ready checks DB+Redis), matching the ECI
# rollout gate and the ml-sync fallback probe: the master runs before the ESS
# rollout and must expose a working app, not just a listening port.
for i in $(seq 1 30); do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://172.24.63.126:8300/health/ready 2>/dev/null || echo 000)
  if [ "$code" = "200" ]; then echo "READY (http 200)"; exit 0; fi
  sleep 2
done
echo "NOT READY after 60s; recent logs:"
docker logs --tail 30 newapi-host 2>&1
exit 1

#!/usr/bin/env bash
#
# Installs NimbusEye on an Ubuntu server.
#
#   sudo bash deploy/install.sh --domain nimbuseye.example.com
#   sudo bash deploy/install.sh --domain nimbuseye.example.com --skip-tls   # no DNS yet
#
# Run from a checkout of the repository. Idempotent: safe to re-run after a
# `git pull` to deploy a new version.
#
# What it does NOT do: create the database. Run deploy/setup-postgres.sh first.

set -euo pipefail

DOMAIN=""
SKIP_TLS=0
PREBUILT=0
EMAIL=""

while [ $# -gt 0 ]; do
  case "$1" in
    --domain)   DOMAIN="$2"; shift 2 ;;
    --email)    EMAIL="$2"; shift 2 ;;
    --skip-tls) SKIP_TLS=1; shift ;;
    --prebuilt) PREBUILT=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "This script needs root. Run: sudo bash $0 --domain <fqdn>" >&2
  exit 1
fi
if [ -z "$DOMAIN" ]; then
  echo "--domain is required, e.g. --domain nimbuseye.example.com" >&2
  exit 2
fi

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BUILD_USER="${SUDO_USER:-root}"
PREFIX=/opt/nimbuseye
ETC=/etc/nimbuseye

echo "==> Repository: $REPO_DIR"
echo "==> Domain:     $DOMAIN"

# ---------------------------------------------------------------- packages
echo "==> Installing packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
PKGS="nginx"
[ "$SKIP_TLS" -eq 1 ] || PKGS="$PKGS certbot python3-certbot-nginx"
apt-get install -y -qq $PKGS >/dev/null
echo "    nginx $(nginx -v 2>&1 | sed 's/.*\///')"

# ------------------------------------------------------------------- build
# With --prebuilt, binaries and the web bundle are expected to be in the payload
# already. That is the normal path for this deployment: the target is a shared
# server running other services, and installing a Go toolchain plus running npm
# on it is both unnecessary and intrusive. Go binaries are statically linked, so
# building on any x86_64 Linux and shipping the result is equivalent.
if [ "$PREBUILT" -eq 1 ]; then
  for b in nimbuseye-api nimbuseye-alerter nimbuseye-collector nimbuseye-prober; do
    [ -x "$REPO_DIR/bin/$b" ] || { echo "--prebuilt: missing $REPO_DIR/bin/$b" >&2; exit 1; }
  done
  [ -f "$REPO_DIR/web/dist/index.html" ] || { echo "--prebuilt: missing web/dist" >&2; exit 1; }
  install -d /tmp/ne-build
  cp "$REPO_DIR"/bin/nimbuseye-* /tmp/ne-build/
  echo "==> Using prebuilt artifacts"
else
  echo "==> Building"
  GO_BIN="$(command -v go || echo "/home/$BUILD_USER/.local/go/bin/go")"
  if [ ! -x "$GO_BIN" ]; then
    echo "Go toolchain not found; pass --prebuilt or install Go." >&2
    exit 1
  fi
  sudo -u "$BUILD_USER" env PATH="$(dirname "$GO_BIN"):$PATH" \
    bash -c "cd '$REPO_DIR' && go build -trimpath -ldflags='-s -w' -o /tmp/ne-build/nimbuseye-api ./cmd/api \
      && go build -trimpath -ldflags='-s -w' -o /tmp/ne-build/nimbuseye-alerter ./cmd/alerter \
      && go build -trimpath -ldflags='-s -w' -o /tmp/ne-build/nimbuseye-collector ./cmd/collector"
  if [ ! -d "$REPO_DIR/web/node_modules" ]; then
    sudo -u "$BUILD_USER" bash -c "cd '$REPO_DIR/web' && npm ci --silent || npm install --silent"
  fi
  sudo -u "$BUILD_USER" bash -c "cd '$REPO_DIR/web' && npm run build >/dev/null"
  echo "    built"
fi

# -------------------------------------------------------------------- user
if ! id -u nimbuseye >/dev/null 2>&1; then
  # System account, no login shell, no home: it only runs services.
  useradd --system --no-create-home --shell /usr/sbin/nologin nimbuseye
  echo "==> Created service user nimbuseye"
fi

# ----------------------------------------------------------------- install
echo "==> Installing to $PREFIX"
install -d -m 755 "$PREFIX/bin" "$PREFIX/web"
install -m 755 /tmp/ne-build/nimbuseye-api      "$PREFIX/bin/"
install -m 755 /tmp/ne-build/nimbuseye-alerter  "$PREFIX/bin/"
install -m 755 /tmp/ne-build/nimbuseye-collector "$PREFIX/bin/"
install -m 755 /tmp/ne-build/nimbuseye-prober    "$PREFIX/bin/"
rm -rf /tmp/ne-build

# Replace the bundle wholesale: leftover hashed assets from an old build would
# otherwise accumulate forever.
rm -rf "$PREFIX/web"/*
cp -r "$REPO_DIR/web/dist/." "$PREFIX/web/"
chown -R root:root "$PREFIX"
find "$PREFIX/web" -type f -exec chmod 644 {} +
find "$PREFIX/web" -type d -exec chmod 755 {} +

install -d -m 750 -o root -g nimbuseye "$ETC"
# Cloud credentials and per-account config: readable only by the service user.
install -d -m 750 -o root -g nimbuseye "$ETC/creds" "$ETC/accounts"

# ------------------------------------------------------------ environment
if [ ! -f "$ETC/nimbuseye.env" ]; then
  DB_ENV="/home/$BUILD_USER/.nimbuseye/db.env"
  if [ ! -f "$DB_ENV" ]; then
    echo "Missing $DB_ENV — run deploy/setup-postgres.sh first." >&2
    exit 1
  fi
  # Only the application DSN is copied. The migration DSN owns the schema and
  # has no business being readable by the running service.
  APP_DSN=$(grep '^NIMBUSEYE_DSN=' "$DB_ENV" | cut -d= -f2-)
  install -m 640 -o root -g nimbuseye /dev/null "$ETC/nimbuseye.env"
  cat > "$ETC/nimbuseye.env" <<EOF
# Generated by deploy/install.sh on $(date -Iseconds)
NIMBUSEYE_DSN=$APP_DSN
EOF
  chmod 640 "$ETC/nimbuseye.env"
  chown root:nimbuseye "$ETC/nimbuseye.env"
  echo "==> Wrote $ETC/nimbuseye.env"
else
  echo "==> $ETC/nimbuseye.env exists, left alone"
fi

if [ ! -f "$ETC/ingest.token" ]; then
  head -c 48 /dev/urandom | base64 | tr -d '\n' > "$ETC/ingest.token"
  chmod 640 "$ETC/ingest.token"
  chown root:nimbuseye "$ETC/ingest.token"
  echo "==> Generated $ETC/ingest.token"
fi

# ----------------------------------------------------------- migrations
echo "==> Applying database migrations"
MIG_DSN=$(grep '^NIMBUSEYE_MIGRATE_DSN=' "/home/$BUILD_USER/.nimbuseye/db.env" | cut -d= -f2-)
"$PREFIX/bin/nimbuseye-api" --migrate --dsn "$MIG_DSN" 2>&1 | sed 's/^/    /'

# -------------------------------------------------------------- systemd
echo "==> Installing systemd units"
install -m 644 "$REPO_DIR/deploy/systemd/nimbuseye-api.service"       /etc/systemd/system/
install -m 644 "$REPO_DIR/deploy/systemd/nimbuseye-alerter.service"   /etc/systemd/system/
install -m 644 "$REPO_DIR/deploy/systemd/nimbuseye-prober.service"    /etc/systemd/system/
install -m 644 "$REPO_DIR/deploy/systemd/nimbuseye-collector@.service" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now nimbuseye-api.service
systemctl enable --now nimbuseye-alerter.service
systemctl enable --now nimbuseye-prober.service
sleep 2
systemctl restart nimbuseye-api.service nimbuseye-alerter.service nimbuseye-prober.service

for svc in nimbuseye-api nimbuseye-alerter nimbuseye-prober; do
  if systemctl is-active --quiet "$svc"; then
    echo "    $svc: active"
  else
    echo "    $svc: FAILED" >&2
    journalctl -u "$svc" -n 15 --no-pager | sed 's/^/      /' >&2
    exit 1
  fi
done

# ---------------------------------------------------------------- nginx
echo "==> Configuring nginx"
# Interim basic auth is gone: the application issues session cookies and its
# middleware denies by default, so a second password in front of it would only be
# something else to forget.

NGINX_CONF=/etc/nginx/conf.d/nimbuseye.conf
[ -f "$NGINX_CONF" ] && cp "$NGINX_CONF" "$NGINX_CONF.bak.$(date +%s)"

# write_http_only strips the TLS server block and serves everything on port 80.
# Used both for --skip-tls and as the bootstrap step before a certificate exists:
# a config naming a certificate file that is not there yet fails nginx -t, and
# certbot cannot obtain one without a working nginx.
write_http_only() {
  sed "s/SERVER_NAME/$DOMAIN/g" "$REPO_DIR/deploy/nginx/nimbuseye.conf" > "$NGINX_CONF"
  python3 - "$NGINX_CONF" <<'PYEOF'
import re, sys
p = sys.argv[1]
s = open(p).read()
# Keep the http-context directives: the limit_req_zone declarations live above the
# server blocks, and dropping them leaves limit_req pointing at an undeclared
# zone, which nginx rejects with "zero size shared memory zone".
head = s[:s.index("server {")]
block = s[s.index("server {\n    listen 443"):]
block = block.replace("listen 443 ssl;", "listen 80;", 1)
block = block.replace("listen [::]:443 ssl;", "listen [::]:80;", 1)
block = block.replace("    http2 on;\n", "")
block = re.sub(r"^\s*ssl_[a-z_]+ .*;\n", "", block, flags=re.M)
# HSTS over plain HTTP would tell browsers to use HTTPS that does not exist yet.
block = re.sub(r"^\s*add_header Strict-Transport-Security.*\n", "", block, flags=re.M)
# The ACME challenge must be reachable without credentials, or certbot cannot
# validate the domain.
block = block.replace("    root /opt/nimbuseye/web;", """    location /.well-known/acme-challenge/ {
        auth_basic off;
        root /var/www/html;
    }

    root /opt/nimbuseye/web;""", 1)
open(p, "w").write(head + block)
PYEOF
}

reload_or_rollback() {
  if ! nginx -t 2>&1 | grep -v "http2. directive is deprecated" | sed 's/^/    /'; then
    echo "    nginx config invalid — removing it and leaving the existing sites untouched" >&2
    rm -f "$NGINX_CONF"
    nginx -t >/dev/null 2>&1 && systemctl reload nginx
    exit 1
  fi
  systemctl reload nginx
}

if [ "$SKIP_TLS" -eq 1 ]; then
  echo "    --skip-tls: HTTP only on port 80"
  write_http_only
  reload_or_rollback
  echo "    nginx reloaded, existing sites unaffected"
else
  CERT_DIR="/etc/letsencrypt/live/$DOMAIN"
  if [ ! -f "$CERT_DIR/fullchain.pem" ]; then
    echo "    no certificate yet: serving HTTP to satisfy the ACME challenge"
    install -d -m 755 /var/www/html
    write_http_only
    reload_or_rollback

    echo "    requesting a certificate for $DOMAIN"
    CB="certonly --webroot -w /var/www/html -d $DOMAIN --non-interactive --agree-tos"
    if [ -n "$EMAIL" ]; then CB="$CB -m $EMAIL"; else CB="$CB --register-unsafely-without-email"; fi
    if ! certbot $CB 2>&1 | tail -6 | sed 's/^/      /'; then
      echo "    certificate request failed; leaving the site on HTTP" >&2
      exit 1
    fi
  else
    echo "    certificate already present for $DOMAIN"
  fi

  # Now the certificate exists, so the full TLS config will validate.
  sed "s/SERVER_NAME/$DOMAIN/g" "$REPO_DIR/deploy/nginx/nimbuseye.conf" > "$NGINX_CONF"
  reload_or_rollback
  echo "    HTTPS enabled, existing sites unaffected"
  systemctl enable --now certbot.timer 2>/dev/null || true
fi

echo
echo "Deployed."
echo "  binaries : $PREFIX/bin"
echo "  web      : $PREFIX/web"
echo "  config   : $ETC"
echo "  services : nimbuseye-api, nimbuseye-alerter, nimbuseye-prober"
[ "$SKIP_TLS" -eq 0 ] && echo "  url      : https://$DOMAIN" || echo "  url      : http://$DOMAIN"
echo
echo "To connect a cloud account, add its key and config, then:"
echo "  sudo install -m 600 -o nimbuseye -g nimbuseye key.pem $ETC/creds/oci-prod.pem"
echo "  sudo -e $ETC/accounts/oci-prod.json"
echo "  sudo systemctl enable --now nimbuseye-collector@oci-prod"
echo
echo "Redeploy after a git pull:  sudo bash deploy/install.sh --domain $DOMAIN"

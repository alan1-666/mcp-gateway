#!/bin/sh
set -eu
# Install outside the application release: an application rollback must not
# remove domain renewal while nginx still serves the domain certificate.
status=0
for name in mcp-gateway-domain mcp-gateway-ip; do
  docker run --rm \
    -v /etc/letsencrypt:/etc/letsencrypt \
    -v /var/lib/letsencrypt:/var/lib/letsencrypt \
    -v /var/log/letsencrypt:/var/log/letsencrypt \
    -v /var/www/mcp-gateway-acme:/var/www/mcp-gateway-acme \
    certbot/certbot:v5.4.0 renew --cert-name "$name" --quiet || status=1
done
# Reload successful renewals even if the other certificate failed to renew.
nginx -t
systemctl reload nginx
for name in mcp-gateway-domain mcp-gateway-ip; do
  openssl x509 -checkend 86400 -noout \
    -in "/etc/letsencrypt/live/$name/fullchain.pem" || status=1
done
exit "$status"

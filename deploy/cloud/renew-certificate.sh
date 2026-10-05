#!/bin/sh
set -eu
docker run --rm \
  -v /etc/letsencrypt:/etc/letsencrypt \
  -v /var/lib/letsencrypt:/var/lib/letsencrypt \
  -v /var/log/letsencrypt:/var/log/letsencrypt \
  -v /var/www/mcp-gateway-acme:/var/www/mcp-gateway-acme \
  certbot/certbot:v5.4.0 renew --cert-name mcp-gateway-ip --quiet
nginx -t
systemctl reload nginx
# Fail the unit if renewal leaves less than 24 hours of certificate validity.
openssl x509 -checkend 86400 -noout -in /etc/letsencrypt/live/mcp-gateway-ip/fullchain.pem

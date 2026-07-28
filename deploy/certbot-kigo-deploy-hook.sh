#!/bin/sh
set -eu

expected_lineage="/etc/letsencrypt/live/106.53.170.243"
lineage=${RENEWED_LINEAGE:-$expected_lineage}

if [ "$lineage" != "$expected_lineage" ]; then
  exit 0
fi

install -d -m 0750 -o root -g kigo /etc/kigo/tls
install -m 0644 -o root -g kigo "$lineage/fullchain.pem" /etc/kigo/tls/server.crt
install -m 0640 -o root -g kigo "$lineage/privkey.pem" /etc/kigo/tls/server.key

systemctl restart kigo-public.service

#/usr/bin/env bash

## setup config.yaml
mkdir -p /etc/mirrorzd
echo '{}' > /etc/mirrorzd/config.yaml
chown mirrorz:mirrorz /etc/mirrorzd/config.yaml
chmod 600 /etc/mirrorzd/config.yaml

## set /var/log
mkdir -p /var/log/mirrorzd
chown mirrorz:mirrorz /var/log/mirrorzd

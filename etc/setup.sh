#/usr/bin/env bash

## setup config.json
mkdir -p /etc/mirrorzd
echo '{}' > /etc/mirrorzd/config.json
chown mirrorz:mirrorz /etc/mirrorzd/config.json
chmod 600 /etc/mirrorzd/config.json

## set /var/log
mkdir -p /var/log/mirrorz
chown mirrorz:mirrorz /var/log/mirrorz

## setup config.json
mkdir -p /etc/ipasn
echo '{}' > /etc/ipasn/config.json
chown mirrorz:mirrorz /etc/ipasn/config.json
chmod 600 /etc/ipasn/config.json

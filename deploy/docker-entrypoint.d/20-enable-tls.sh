#!/bin/sh
# 証明書がマウントされていれば HTTPS を有効にする。
# 無ければ HTTP のままにする。証明書の有無で構成を切り替えたいので、
# nginx の設定ファイルを条件付きで配置する方式にしている。
set -e

CERT=/etc/nginx/certs/fullchain.pem
KEY=/etc/nginx/certs/privkey.pem

if [ -s "$CERT" ] && [ -s "$KEY" ]; then
    cp /etc/nginx/tls.conf.disabled /etc/nginx/conf.d/tls.conf
    echo "TLS: 証明書を検出したので HTTPS を有効にしました"
else
    echo "TLS: 証明書が無いので HTTP のみで起動します (make cert または tailscale cert)"
fi

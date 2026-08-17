#!/bin/sh
set -eu

site_name=$(printf %s "${HOOKFLY_SITE_NAME:-Hookfly}" | tr '\r\n' ' ')
escaped_site_name=$(printf %s "$site_name" | sed 's/\\/\\\\/g; s/"/\\"/g; s/</\\u003c/g; s/>/\\u003e/g; s/&/\\u0026/g')
printf 'window.HOOKFLY_CONFIG = { siteName: "%s" };\n' "$escaped_site_name" > /usr/share/nginx/html/runtime-config.js

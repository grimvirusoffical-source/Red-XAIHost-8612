# red-xai.com Hub source

This directory is the tracked source for the static Red-XAI Hub currently served by the `redx-hub` service.

Production target: `/opt/redx-hub/www/index.html`.

The Hub deliberately uses same-site routes (`/host`, `/db`, `/vortex`) instead of raw server IPs. Vortex release versions are resolved by the website's `/api/releases/vortex` endpoint so the homepage never hardcodes a stale application release.

The page also embeds `/redxai-connect/ux.js` so login-scoped analytics and notification consent works on the Hub itself.

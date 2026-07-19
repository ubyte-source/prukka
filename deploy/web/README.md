# Hosted dashboard (prukka.ubyte.it)

The hosted control plane is the **same static bundle** the daemon embeds.
Deploying it is a copy:

```bash
rsync -av internal/webui/dist/ user@server:/var/www/prukka.ubyte.it/
```

Serve it over https with any static server. No backend, no database: the
page drives the visitor's **own local daemon** at `http://127.0.0.1:8080`,
which allows that origin via `daemon.cors_origin` (default
`https://prukka.ubyte.it`). The current bundle does not intentionally upload
audio, transcripts or the control token to the static host. The hosting origin
still controls executable JavaScript trusted to call the local daemon, so a
compromised host or third-party script can access data available to the page.
Serve only reviewed immutable assets and treat that origin as privileged.

The built `index.html` declares that endpoint explicitly in the
`prukka-api-base` metadata. The daemon replaces it with `same-origin` when it
serves the embedded UI, including on an operator-configured LAN bind. For a
hosted deployment using a different daemon address, change the metadata, the
daemon's `cors_origin` and the CSP `connect-src` together; do not infer the API
from the dashboard hostname.

Example nginx block:

```nginx
server {
    listen 443 ssl;
    server_name prukka.ubyte.it;
    root /var/www/prukka.ubyte.it;
    index index.html;
    add_header Cache-Control "public, max-age=300" always;
    add_header Content-Security-Policy "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src http://127.0.0.1:8080" always;
    add_header Permissions-Policy "camera=(), microphone=(), geolocation=()" always;
    add_header Referrer-Policy "no-referrer" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "DENY" always;
    add_header Strict-Transport-Security "max-age=31536000" always;
}
```

Visitors need a running local daemon (`prukka up` or the installed service)
and, for changes or diagnostics, the control token — `prukka doctor` shows
where it lives.

Chrome 142 and later also gate public-site requests to loopback/local addresses
behind [Local Network Access permission](https://developer.chrome.com/blog/local-network-access).
The visitor must allow the prompt for the hosted origin. If it was denied,
restore the local-network permission in that site's browser settings and reload;
the dashboard reports this alongside the daemon-start hint when `fetch` fails.
The permission is available only to secure contexts, so serve the hosted page
over HTTPS. Browser behavior is not assumed to be identical, and hosted mode
does not replace testing on the browsers supported by the deployment. The
embedded `/ui/` mode avoids the public-origin-to-loopback hop and remains the
recommended operational path. Chrome's former Private Network Access response
header experiment is not the mechanism here; Local Network Access replaced it.

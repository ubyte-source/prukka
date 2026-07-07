# Hosted dashboard (prukka.ubyte.it)

The hosted control plane is the **same static bundle** the daemon embeds
Deploying it is a copy:

```bash
rsync -av internal/webui/dist/ user@server:/var/www/prukka.ubyte.it/
```

Serve it over https with any static server. No backend, no database: the
page drives the visitor's **own local daemon** at `http://127.0.0.1:8080`,
which allows that origin via `daemon.cors_origin` (default
`https://prukka.ubyte.it`). Audio, transcripts, keys and tokens never touch
this server.

Example nginx block:

```nginx
server {
    listen 443 ssl;
    server_name prukka.ubyte.it;
    root /var/www/prukka.ubyte.it;
    index index.html;
    add_header Cache-Control "public, max-age=300";
}
```

Visitors need a running local daemon (`prukka up` or the installed service)
and, for changes, the control token — `prukka doctor` shows where it lives.

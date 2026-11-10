# Running HelpBuoy behind a reverse proxy

The apps connect to `wss://host/helpbuoy` and run SSH inside the WebSocket. In its default mode (`--mode http`), `helpbuoy-server` listens on plain HTTP on `127.0.0.1:5959` and leaves HTTPS to a reverse proxy in front of it: nginx, Apache or HAProxy. This page shows how to put several HelpBuoy servers behind one proxy and one certificate, each on its own host name:

| Host name | Server |
| --- | --- |
| `acme.help.example.com` | one helpdesk |
| `beta.help.example.com` | another helpdesk |

Users type the host name in the app, without a port. Every configuration below was tested with nginx 1.27, Apache 2.4 and HAProxy 3.0.

## What the proxy must do

- Serve HTTPS on port 443 and pass the path `/helpbuoy` on to the server as a WebSocket (HTTP/1.1 with `Upgrade`). Nothing else on the host name needs to reach the server, so the same host name can also serve a website.
- Keep the `Host` header. The apps name the server only by host name: the path is always `/helpbuoy`, so the proxy (or the server, with virtual hosts) picks the server by host name, never by path.
- Add `X-Forwarded-For`. The server takes the client address from it for its limits per IP and its logs, but only on connections from a trusted proxy (`--trusted-proxy`, default `127.0.0.1,::1`). Set that flag when the proxy runs on another machine.
- Allow long idle connections. A computer waiting for a techie keeps its connection open for hours. The apps and the server send a keepalive every 15 seconds, so any timeout above a minute works; the examples use an hour.

Any certificate works for the apps, even a self-signed one, since they identify the server by its pinned SSH host key and not by the certificate. A certificate that browsers trust is still the sensible choice.

## One certificate for all host names

Give all helpdesks host names under one domain and use one wildcard certificate for `*.help.example.com`. Let's Encrypt issues wildcard certificates only with the DNS-01 challenge, for example with a certbot DNS plugin for your DNS provider:

    certbot certonly --dns-cloudflare --dns-cloudflare-credentials /etc/letsencrypt/cloudflare.ini \
        -d 'help.example.com' -d '*.help.example.com'

With a wildcard DNS record (`*.help.example.com` pointing at the proxy), adding a helpdesk needs no DNS or certificate change.

When the host names are not under one domain (`help.acme.com`, `support.beta.org`), list them all in one certificate instead, `-d help.acme.com -d support.beta.org`, which also works with the HTTP-01 challenge. The configurations stay the same apart from the names.

HAProxy wants the certificate, its chain and its key in one file. Make it again after every renewal, for example in a certbot deploy hook:

    cat /etc/letsencrypt/live/help.example.com/fullchain.pem \
        /etc/letsencrypt/live/help.example.com/privkey.pem > /etc/haproxy/certs/help.example.com.pem
    systemctl reload haproxy

## Option 1: one server with virtual hosts

The simplest setup for many helpdesks is one `helpbuoy-server` process with virtual hosts. Every host name goes to the same backend, and the server picks the helpdesk from the `Host` header. Each virtual host has its own host key, techies, computers and log, and the server refuses host names it doesn't know.

    export HELPBUOY_SERVER_VHOSTS=/var/lib/helpbuoy-server/vhosts
    helpbuoy-server vhost add acme.help.example.com
    helpbuoy-server info --vhost acme.help.example.com --company "Acme" --phone "+1 234 567 890"
    helpbuoy-server techie add --vhost acme.help.example.com alice 'ssh-ed25519 AAAA...'
    helpbuoy-server serve

Adding a helpdesk later is `vhost add` again; the running server picks it up within a second, and the proxy needs no change. These configurations also serve a single server without virtual hosts: just use one host name.

### nginx

```nginx
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name *.help.example.com;

    ssl_certificate     /etc/letsencrypt/live/help.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/help.example.com/privkey.pem;

    location /helpbuoy {
        proxy_pass http://127.0.0.1:5959;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
    }
}
```

### Apache

Apache 2.4.47 or later with `mod_ssl`, `mod_proxy` and `mod_proxy_http`. Unknown host names reach the server, which answers 404.

```apache
<VirtualHost *:443>
    ServerName help.example.com
    ServerAlias *.help.example.com
    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/help.example.com/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/help.example.com/privkey.pem
    ProxyPreserveHost On
    ProxyPass /helpbuoy http://127.0.0.1:5959/helpbuoy upgrade=websocket timeout=3600
</VirtualHost>
```

### HAProxy

```haproxy
defaults
    mode http
    timeout connect 5s
    timeout client 1h
    timeout server 1h
    timeout tunnel 1h

frontend https
    bind :443 ssl crt /etc/haproxy/certs/help.example.com.pem
    bind :::443 v6only ssl crt /etc/haproxy/certs/help.example.com.pem
    option forwardfor
    http-request return status 404 if !{ path /helpbuoy }
    default_backend helpbuoy

backend helpbuoy
    server helpbuoy 127.0.0.1:5959
```

## Option 2: separate servers

Run separate `helpbuoy-server` processes when the helpdesks must not share a process, for example to run them as different system users or to update them one at a time. Each gets its own state directory and port:

    helpbuoy-server serve --state /var/lib/helpbuoy-acme --listen 127.0.0.1:5959
    helpbuoy-server serve --state /var/lib/helpbuoy-beta --listen 127.0.0.1:5960

The proxy then picks the port by host name, and answers 404 for host names without a server.

### nginx

The `map` holds one line per server.

```nginx
# Which helpbuoy-server serves which host name.
map $host $helpbuoy {
    acme.help.example.com  127.0.0.1:5959;
    beta.help.example.com  127.0.0.1:5960;
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name *.help.example.com;

    # One wildcard certificate for every host name.
    ssl_certificate     /etc/letsencrypt/live/help.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/help.example.com/privkey.pem;

    location /helpbuoy {
        if ($helpbuoy = "") {
            return 404;
        }
        proxy_pass http://$helpbuoy;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
    }
}
```

### Apache

Apache 2.4.47 or later with `mod_ssl`, `mod_proxy`, `mod_proxy_http` and `mod_alias`. One virtual host per server, all with the same certificate. The first virtual host is Apache's default for names it doesn't know, so it answers 404.

```apache
# Needs mod_ssl, mod_proxy, mod_proxy_http and mod_alias (Apache 2.4.47 or later).

# The first virtual host catches host names that have no server: 404.
<VirtualHost *:443>
    ServerName unknown.help.example.com
    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/help.example.com/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/help.example.com/privkey.pem
    Redirect 404 /
</VirtualHost>

<VirtualHost *:443>
    ServerName acme.help.example.com
    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/help.example.com/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/help.example.com/privkey.pem
    ProxyPreserveHost On
    ProxyPass /helpbuoy http://127.0.0.1:5959/helpbuoy upgrade=websocket timeout=3600
</VirtualHost>

<VirtualHost *:443>
    ServerName beta.help.example.com
    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/help.example.com/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/help.example.com/privkey.pem
    ProxyPreserveHost On
    ProxyPass /helpbuoy http://127.0.0.1:5960/helpbuoy upgrade=websocket timeout=3600
</VirtualHost>
```

### HAProxy

The map file `/etc/haproxy/helpbuoy.map` holds one line per server, naming its backend:

```
# host name               backend
acme.help.example.com     acme
beta.help.example.com     beta
```

```haproxy
defaults
    mode http
    timeout connect 5s
    timeout client 1h
    timeout server 1h
    timeout tunnel 1h

frontend https
    # One file with the wildcard certificate, its chain and its key.
    bind :443 ssl crt /etc/haproxy/certs/help.example.com.pem
    bind :::443 v6only ssl crt /etc/haproxy/certs/help.example.com.pem
    option forwardfor
    # The backend for this host name, from the map file.
    http-request set-var(req.backend) req.hdr(host),field(1,:),lower,map(/etc/haproxy/helpbuoy.map)
    http-request return status 404 if !{ path /helpbuoy } || !{ var(req.backend) -m found }
    use_backend %[var(req.backend)]

backend acme
    server acme 127.0.0.1:5959

backend beta
    server beta 127.0.0.1:5960
```

## Checking

The WebSocket upgrade should come back as `101 Switching Protocols`:

    curl -si --http1.1 --max-time 3 https://acme.help.example.com/helpbuoy \
        -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' \
        -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H 'Sec-WebSocket-Protocol: helpbuoy-ssh' | head -1

A `404` means the proxy or the server doesn't know the host name, a `502` or `503` that the server isn't running on the port the proxy uses. Once an app connects, the `auth ok` lines in the server log (`server.log` in the state directory) should show the app's own address as `ip`, not the proxy's; if they show the proxy's, add the proxy's address to `--trusted-proxy`.

## Without a proxy

The server can also serve HTTPS itself (`--mode https`, with `--cert`/`--key`, `--acme` or a self-signed certificate), or plain SSH without WebSocket and TLS (`--mode ssh`, by default on port 5959). Exactly one mode is on per server. The apps always try HTTPS first and fall back to plain SSH, so users type the same kind of address for every mode: the host name, or `host:port` when the server is not on port 443. See `helpbuoy-server help`.

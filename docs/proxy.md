# Running HelpBuoy behind a reverse proxy

The apps connect to `wss://host/helpbuoy` and run SSH inside the WebSocket. With `--mode http`, `helpbuoy-server` listens on plain HTTP on `127.0.0.1:5959` and leaves HTTPS to a reverse proxy in front of it: nginx, Apache or HAProxy. This page shows how to put several helpdesks behind one proxy, in two ways:

| Setup | Users type | The apps connect to |
| --- | --- | --- |
| One server with virtual hosts | `relay.helpbuoy.example.com/acme` | `wss://relay.helpbuoy.example.com/acme` |
| Separate servers | `acme.helpbuoy.example.com` | `wss://acme.helpbuoy.example.com/helpbuoy` |

Users type the address in the app, without a port.

## What the proxy must do

- Serve HTTPS on port 443 and pass the requests on to the server as a WebSocket (HTTP/1.1 with `Upgrade`), without changing the path. A server without virtual hosts only needs the path `/helpbuoy`, so its host name can also serve a website. With virtual hosts every helpdesk has its own path (`/acme`), so give the server a host name of its own and pass everything on.
- Add `X-Forwarded-For`. The server takes the client address from it for its limits per IP and its logs, but only on connections from a trusted proxy (`--trusted-proxy`, default `127.0.0.1,::1`). Set that flag when the proxy runs on another machine.
- Allow long idle connections. A computer waiting for a techie keeps its connection open for hours. The apps and the server send a keepalive every 15 seconds, so any timeout above a minute works; the examples use an hour.

Any certificate works for the apps, even a self-signed one, since they identify the server by its pinned SSH host key and not by the certificate. A certificate that browsers trust is still the sensible choice.

## Option 1: one server with virtual hosts

The simplest setup for many helpdesks is one `helpbuoy-server` process with virtual hosts, on one host name with one ordinary certificate. The server picks the helpdesk from the path: `relay.helpbuoy.example.com/acme` is the virtual host `acme`, and the apps open their WebSocket on exactly that address. A browser that opens it gets a line of text. Each virtual host has its own host key, techies, computers and log, and the server refuses names it doesn't know. The host name plays no part in this.

    export HELPBUOY_SERVER_VHOSTS=/var/lib/helpbuoy-server/vhosts
    helpbuoy-server vhost add acme
    helpbuoy-server info --vhost acme --company "Acme" --phone "+1 234 567 890"
    helpbuoy-server techie add --vhost acme alice 'ssh-ed25519 AAAA...'
    helpbuoy-server serve --mode http

Adding a helpdesk later is `vhost add` again; the running server picks it up within a second, and the proxy, DNS and certificate need no change. A single server without virtual hosts, whose address is just the host name, only needs `/helpbuoy` passed on, as in option 2.

### nginx

```nginx
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name relay.helpbuoy.example.com;

    ssl_certificate     /etc/letsencrypt/live/relay.helpbuoy.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/relay.helpbuoy.example.com/privkey.pem;

    # Every path is a helpdesk. No URI after the port, so the path is kept.
    location / {
        proxy_pass http://127.0.0.1:5959;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
    }
}
```

### Apache

Apache 2.4.47 or later with `mod_ssl`, `mod_proxy` and `mod_proxy_http`. Unknown names reach the server, which answers 404.

```apache
<VirtualHost *:443>
    ServerName relay.helpbuoy.example.com
    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/relay.helpbuoy.example.com/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/relay.helpbuoy.example.com/privkey.pem
    ProxyPass / http://127.0.0.1:5959/ upgrade=websocket timeout=3600
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
    bind :443 ssl crt /etc/haproxy/certs/relay.helpbuoy.example.com.pem
    bind :::443 v6only ssl crt /etc/haproxy/certs/relay.helpbuoy.example.com.pem
    option forwardfor
    default_backend helpbuoy

backend helpbuoy
    server helpbuoy 127.0.0.1:5959
```

## Option 2: separate servers

Run separate `helpbuoy-server` processes when the helpdesks must not share a process, for example to run them as different system users or to update them one at a time. Each gets its own state directory and port:

    helpbuoy-server serve --mode http --state /var/lib/helpbuoy-acme --listen 127.0.0.1:5959
    helpbuoy-server serve --mode http --state /var/lib/helpbuoy-beta --listen 127.0.0.1:5960

The proxy then picks the port by host name, and answers 404 for host names without a server. Every helpdesk has its own host name here, and the path is always `/helpbuoy`.

### One certificate for all host names

Give all helpdesks host names under one domain and use one wildcard certificate for `*.helpbuoy.example.com`. Let's Encrypt issues wildcard certificates only with the DNS-01 challenge, for example with a certbot DNS plugin for your DNS provider:

    certbot certonly --dns-cloudflare --dns-cloudflare-credentials /etc/letsencrypt/cloudflare.ini \
        -d 'helpbuoy.example.com' -d '*.helpbuoy.example.com'

With a wildcard DNS record (`*.helpbuoy.example.com` pointing at the proxy), adding a helpdesk needs no DNS or certificate change.

When the host names are not under one domain (`help.acme.com`, `support.beta.org`), list them all in one certificate instead, `-d help.acme.com -d support.beta.org`, which also works with the HTTP-01 challenge. The configurations stay the same apart from the names.

HAProxy wants the certificate, its chain and its key in one file (also in option 1). Make it again after every renewal, for example in a certbot deploy hook:

    cat /etc/letsencrypt/live/helpbuoy.example.com/fullchain.pem \
        /etc/letsencrypt/live/helpbuoy.example.com/privkey.pem > /etc/haproxy/certs/helpbuoy.example.com.pem
    systemctl reload haproxy

### nginx

The `map` holds one line per server.

```nginx
# Which helpbuoy-server serves which host name.
map $host $helpbuoy {
    acme.helpbuoy.example.com  127.0.0.1:5959;
    beta.helpbuoy.example.com  127.0.0.1:5960;
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name *.helpbuoy.example.com;

    # One wildcard certificate for every host name.
    ssl_certificate     /etc/letsencrypt/live/helpbuoy.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/helpbuoy.example.com/privkey.pem;

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
    ServerName unknown.helpbuoy.example.com
    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/helpbuoy.example.com/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/helpbuoy.example.com/privkey.pem
    Redirect 404 /
</VirtualHost>

<VirtualHost *:443>
    ServerName acme.helpbuoy.example.com
    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/helpbuoy.example.com/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/helpbuoy.example.com/privkey.pem
    ProxyPreserveHost On
    ProxyPass /helpbuoy http://127.0.0.1:5959/helpbuoy upgrade=websocket timeout=3600
</VirtualHost>

<VirtualHost *:443>
    ServerName beta.helpbuoy.example.com
    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/helpbuoy.example.com/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/helpbuoy.example.com/privkey.pem
    ProxyPreserveHost On
    ProxyPass /helpbuoy http://127.0.0.1:5960/helpbuoy upgrade=websocket timeout=3600
</VirtualHost>
```

### HAProxy

The map file `/etc/haproxy/helpbuoy.map` holds one line per server, naming its backend:

```
# host name               backend
acme.helpbuoy.example.com     acme
beta.helpbuoy.example.com     beta
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
    bind :443 ssl crt /etc/haproxy/certs/helpbuoy.example.com.pem
    bind :::443 v6only ssl crt /etc/haproxy/certs/helpbuoy.example.com.pem
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

    curl -si --http1.1 --max-time 3 https://relay.helpbuoy.example.com/acme \
        -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' \
        -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H 'Sec-WebSocket-Protocol: helpbuoy-ssh' | head -1

With separate servers the URL is `https://acme.helpbuoy.example.com/helpbuoy`. A `404` means the proxy doesn't know the host name or the server doesn't know the name, a `502` or `503` that the server isn't running on the port the proxy uses. Once an app connects, the `auth ok` lines in the server log (`server.log` in the state directory) should show the app's own address as `ip`, not the proxy's; if they show the proxy's, add the proxy's address to `--trusted-proxy`.

## Without a proxy

The server can also serve HTTPS itself (`--mode https`, with `--cert`/`--key`, `--acme` or a self-signed certificate), or plain SSH without WebSocket and TLS (`--mode ssh`, the default, on port 5959). Exactly one mode is on per server. The apps always try HTTPS first and fall back to plain SSH, so users type the same kind of address for every mode: the host name, or `host:port` when the server is not on port 443. Virtual hosts, with `/name` after it, need http or https mode. See `helpbuoy-server help`.

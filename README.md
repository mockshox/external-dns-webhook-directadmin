# external-dns-webhook-directadmin

Webhook that connects [ExternalDNS](https://kubernetes-sigs.github.io/external-dns/) to a [DirectAdmin](https://docs.directadmin.com/developer/api/) panel. It creates and deletes A, AAAA, CNAME, and TXT records through `CMD_API_DNS_CONTROL`.

The webhook is deployed by the [ExternalDNS Helm chart](https://kubernetes-sigs.github.io/external-dns/latest/charts/external-dns/) and runs as a sidecar. There is no separate chart. It speaks the [webhook provider API](https://kubernetes-sigs.github.io/external-dns/latest/docs/tutorials/webhook-provider/).

[cyber_Folks](https://cyberfolks.pl/) is one panel this image is used with. Other DirectAdmin hosts should work the same way; they have not been tested.

Images are published for `linux/amd64` and `linux/arm64` at `ghcr.io/mockshox/external-dns-webhook-directadmin`. The runtime image is distroless and has no shell. A release build is signed with keyless cosign.

## Motivation
I built this to automate DNS management for my Kubernetes workloads without moving my DNS away from cyber_Folks. ExternalDNS handles the Kubernetes side, and this webhook bridges it to DirectAdmin, so I don’t have to create and delete records manually.

It uses the DirectAdmin API rather than anything specific to cyber_Folks, so it should also be useful for people using other providers running the same panel.

## Quick start

1. In DirectAdmin, create a login key that allows only the command `CMD_API_DNS_CONTROL`.
2. Create a secret in the ExternalDNS namespace:

```yaml
# secret.yml
apiVersion: v1
kind: Secret
metadata:
  name: directadmin-dns
  namespace: external-dns
stringData:
  DA_URL: https://server.example:2222
  DA_USERNAME: user
  DA_LOGIN_KEY: <login-key>
  DA_DOMAIN: example.com
```

`DA_URL` is the https panel origin, including the port, with no path. Set `DA_ALLOW_HTTP=true` only when the panel has no TLS.

3. Configure the [ExternalDNS values](https://kubernetes-sigs.github.io/external-dns/latest/charts/external-dns/#values). `policy` is required by current charts. `registry: txt` and a `txtOwnerId` keep this instance from deleting records it does not own. `managedRecordTypes` must include `TXT` or the ownership records are never written.

```yaml
# values.yml
policy: sync
registry: txt
txtOwnerId: example
domainFilters:
  - example.com
sources:
  - ingress
managedRecordTypes:
  - A
  - AAAA
  - CNAME
  - TXT
provider:
  name: webhook
  webhook:
    image:
      repository: ghcr.io/mockshox/external-dns-webhook-directadmin
      # Image tags are X.Y.Z from git tag vX.Y.Z. CI does not publish latest.
      tag: "0.0.3"
    env:
      - name: DA_URL
        valueFrom:
          secretKeyRef:
            name: directadmin-dns
            key: DA_URL
      - name: DA_USERNAME
        valueFrom:
          secretKeyRef:
            name: directadmin-dns
            key: DA_USERNAME
      - name: DA_LOGIN_KEY
        valueFrom:
          secretKeyRef:
            name: directadmin-dns
            key: DA_LOGIN_KEY
      - name: DA_DOMAIN
        valueFrom:
          secretKeyRef:
            name: directadmin-dns
            key: DA_DOMAIN
      - name: DA_TTL
        value: "60"
    resources:
      requests:
        cpu: 10m
        memory: 32Mi
      limits:
        memory: 128Mi
    readinessProbe:
      httpGet:
        path: /ready
        port: http-webhook
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      runAsUser: 65532
      runAsGroup: 65532
      capabilities:
        drop: ["ALL"]
extraArgs:
  webhook-provider-url: http://127.0.0.1:8888
  ttl: 60s
  webhook-provider-write-timeout: 3m
```

The webhook listens on `127.0.0.1:8888`. Port 8080 is probes and metrics, not the DNS API. Each record is a separate panel call, so the default 10 second write timeout is too short once the zone has more than a few names.

Keep `replicaCount` at 1. The chart already refuses a higher value. Run one ExternalDNS deployment per zone. Two controllers writing the same zone can interleave updates.

4. Add the ExternalDNS Helm repository:

```bash
helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/
helm repo update external-dns
```

5. Install the chart:

```bash
kubectl apply -f secret.yml
helm upgrade --install external-dns external-dns/external-dns \
  --namespace external-dns \
  --create-namespace \
  -f values.yml
```

For more details about installing ExternalDNS, see the [chart documentation](https://kubernetes-sigs.github.io/external-dns/latest/charts/external-dns/).

### cyber_Folks

Set `DA_URL` to the panel, for example `https://<server>.cyber-folks.pl:2223`. The zone's authoritative servers are `ns1.cyberfolks.pl` and `ns2.cyberfolks.pl`. Leave `DA_INSECURE` unset when the panel certificate chains to a public CA.

### Gateway API

Replace `sources` with `gateway-httproute` and set `gatewayNamespace` when records come from HTTPRoutes. Add the Gateway RBAC rules from the [chart values](https://kubernetes-sigs.github.io/external-dns/latest/charts/external-dns/#values). A CNAME and its owner TXT cannot share a name, so a hostname target needs `txtPrefix`.

## Configuration

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `DA_URL` | yes | | `https` panel origin, including the port, with no path |
| `DA_USERNAME` | yes | | DirectAdmin user |
| `DA_LOGIN_KEY` | yes | | Login key sent as the Basic-auth password |
| `DA_DOMAINS` | one of these | | Comma-separated zones. A name uses the longest matching suffix |
| `DA_DOMAIN` | one of these | | Single zone, used when `DA_DOMAINS` is empty |
| `DOMAIN_FILTER` | | all configured zones | Zones to announce on `GET /`. Each entry must equal a configured zone |
| `DA_INSECURE` | | false | `true` skips TLS verification. Prefer a mounted CA and `SSL_CERT_FILE` |
| `DA_ALLOW_HTTP` | | false | `true` permits an `http://` panel URL. The login key is then sent in cleartext |
| `SSL_CERT_FILE` | | system pool | PEM bundle for a private CA. Mount the file and set this to its path |
| `DA_TTL` | | 60 | Record TTL in seconds when ExternalDNS omits `recordTTL`. `0` leaves the panel default |
| `LOG_LEVEL` | | `info` | `debug`, `info`, `warn`, or `error`. Info logs the record type and name. Record values are debug only |
| `WEBHOOK_ADDRESS` | | `127.0.0.1:8888` | ExternalDNS API |
| `METRICS_ADDRESS` | | `:8080` | `/healthz`, `/ready`, and `/metrics` |

Zones are matched case-insensitively. AAAA targets are stored in compressed form. A name outside the configured zones fails the apply.

## Records

Supported types are A, AAAA, CNAME, and TXT. DirectAdmin stores a new record at 14400 seconds unless a TTL is sent. Set ExternalDNS `--ttl=60s` and `DA_TTL` to the same value. An endpoint `recordTTL` wins over `DA_TTL`. A same-target TTL change uses `action=edit`, so the name stays in place. If the panel rejects `edit`, that one value is deleted and added again. A different target is added before the old value is removed.

When a zone dump includes `ttl`, that value is listed. A name-to-value entry can carry it as `{"value":"...","ttl":60}`. When the dump omits TTL and `DA_TTL` is set, the list reports `DA_TTL`, so a matching ExternalDNS `--ttl` does not rewrite the record on every sync. `DA_TTL=0` leaves an omitted TTL as 0.

An add that reports the record already exists, and a delete that reports it missing, count as success. Transport errors and panel HTTP 429 and 5xx are retried twice. A 429 waits for `Retry-After`, plus a little jitter, and never longer than 15 seconds. Other retries wait 200ms and then 400ms, also with jitter. Redirects are not followed. A write body that is neither a JSON result with `error` or `success`, nor an `error=` query string, fails the apply. One name failing does not skip the rest of the plan. Those failures are returned to ExternalDNS as 502 or 504, which it treats as a soft error.

A zone dump that is not JSON, or JSON that is not a known records container, fails the list. ExternalDNS retries that failure instead of treating the zone as empty. An empty `records` array, an empty `dnsrecs` object, or an empty `A`, `AAAA`, `CNAME`, or `TXT` map is an empty zone. A dump that only contains unsupported types, such as MX or NS, is an empty managed set. Those records stay in place.

Deletes send `action=select` with a raw `name=...&value=...` selector. DirectAdmin receives that pair encoded once. Encoding it before `url.Values` makes the panel look up a literal `%3D` and leave the record in place.

## Probes and metrics

`/healthz` on port 8080 is liveness. It stays down until the process is listening, and stays up when the panel is unreachable. `/ready` stays down until a zone list succeeds. It drops again when a list fails, or when an apply cannot reach the panel (transport error, redirect, HTTP 401, 403, 429, or 5xx). A record the panel rejects does not clear readiness. The process lists the zones once at startup and retries that list until it succeeds.

The quick start points the chart's readiness probe at `/ready`. Liveness stays on `/healthz`, so a panel outage does not restart the container. The chart's default is to probe `/healthz` for both.

`/metrics` on port 8080 includes the Go process metrics, DirectAdmin call counts and latency, the last successful list and apply, and `webhook_build_info`.

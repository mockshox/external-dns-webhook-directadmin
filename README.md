# external-dns-webhook-directadmin
ExternalDNS webhook that creates and deletes A, AAAA, CNAME, and TXT records in DirectAdmin DNS, including cyber_Folks.

The webhook speaks [DirectAdmin](https://docs.directadmin.com/developer/api/) `CMD_API_DNS_CONTROL`. cyber_Folks is one host that runs that panel. Certificate DNS-01 stays on [cert-manager-webhook-dns-lexicon](https://github.com/lanefu/cert-manager-webhook-dns-lexicon) with `provider: directadmin`. This image does not issue certificates.

## Run

ExternalDNS chart 1.21 probes port 8080 on the pod IP. This process serves `/healthz` there. The ExternalDNS API listens on `127.0.0.1:8888`, which is ExternalDNS's default webhook URL. The chart's ClusterIP also publishes port 8080; that port is the health check, not the DNS API.

```yaml
provider:
  name: webhook
  webhook:
    image:
      repository: ghcr.io/mockshox/external-dns-webhook-directadmin
      tag: 0.1.0
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
        value: example.com
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
```

Create the login key with command `CMD_API_DNS_CONTROL` only. Set `managedRecordTypes` to include `TXT` when ExternalDNS uses a TXT registry.

### cyber_Folks

Point `DA_URL` at the panel, for example `https://<server>.cyber-folks.pl:2223`. The zone's authoritative servers are `ns1.cyberfolks.pl` and `ns2.cyberfolks.pl`. Leave `DA_INSECURE` unset when the panel certificate chains to a public CA.

## Configuration

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `DA_URL` | yes | | Panel origin, including the port, with no path |
| `DA_USERNAME` | yes | | DirectAdmin user |
| `DA_LOGIN_KEY` | yes | | Login key sent as the Basic-auth password |
| `DA_DOMAINS` | one of these | | Comma-separated zones. A name uses the longest matching suffix |
| `DA_DOMAIN` | one of these | | Single zone, used when `DA_DOMAINS` is empty |
| `DOMAIN_FILTER` | | all configured zones | Zones to announce on `GET /`. Each entry must equal a configured zone |
| `DA_INSECURE` | | false | `true` skips TLS verification |
| `DA_TTL` | | 60 | Record TTL in seconds when ExternalDNS omits `recordTTL`. `0` leaves the panel default |
| `WEBHOOK_ADDRESS` | | `127.0.0.1:8888` | ExternalDNS API |
| `HEALTH_ADDRESS` | | `0.0.0.0:8080` | `/healthz` for the chart probe |

Supported types are A, AAAA, CNAME, and TXT. A name outside the configured zones fails the apply. A zone dump that is not JSON fails the list, so ExternalDNS retries instead of treating the zone as empty.

Deletes send `action=select` with a raw `name=...&value=...` selector. DirectAdmin receives that pair encoded once. Encoding it before `url.Values` makes the panel look up a literal `%3D` and leave the record in place.

Creates send `ttl`. An endpoint `recordTTL` wins. Otherwise the webhook uses `DA_TTL`, which defaults to 60 seconds. Set ExternalDNS `--ttl=60s` as well so existing records are reconciled down from the panel default of 14400.

## Development

```sh
go test ./...
```

Pushes to `mbono/work` publish `ghcr.io/mockshox/external-dns-webhook-directadmin:work`. A `v*` tag publishes that version and moves `latest`. The workflow tries to mark the package public; if that call is rejected, set the package visibility in GitHub once.

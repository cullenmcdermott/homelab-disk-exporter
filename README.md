# disk-exporter

A Prometheus exporter that runs as a DaemonSet on Kubernetes nodes, exposing disk health metrics via NVMe SMART data (direct ioctl) and SATA SMART data (via `smartctl`). Also maps Ceph OSD assignments per device.

## Metrics

| Metric | Description |
|--------|-------------|
| `homelab_disk_info` | Disk info: model, serial, firmware, transport, rotational, ceph_osd |
| `homelab_disk_size_bytes` | Disk capacity in bytes |
| `homelab_disk_temperature_celsius` | Current disk temperature |
| `homelab_disk_percentage_used` | NVMe percentage used indicator |
| `homelab_disk_available_spare_percent` | NVMe available spare % |
| `homelab_disk_power_on_hours_total` | Cumulative power-on hours |
| `homelab_disk_data_read_bytes_total` | Total data read |
| `homelab_disk_data_written_bytes_total` | Total data written |
| `homelab_disk_unsafe_shutdowns_total` | Unsafe shutdown count |
| `homelab_disk_media_errors_total` | Media error count |
| `homelab_disk_critical_warning` | NVMe critical warning bitmask |
| `homelab_disk_scrape_duration_seconds` | Collection duration |
| `homelab_disk_devices_total` | Total discovered devices |

## Requirements

- Runs as a privileged DaemonSet (needed for NVMe ioctl and `smartctl`)
- Mounts `/dev`, `/sys`, and `/var/lib/rook` from the host
- `NODE_NAME` env var set via Kubernetes downward API

## Development

```bash
# Activate the flox environment (auto-activates if direnv is configured)
flox activate

# Build
just build

# Test
just test

# Lint
just lint

# Build container image
just docker-build
```

## Deployment

K8s manifests are in `deploy/`. The homelab ArgoCD application references
`k8s/disk-exporter/` in the [homelab repo](https://github.com/cullenmcdermott/home).

## Container Image

```
ghcr.io/cullenmcdermott/disk-exporter:latest
```

Released automatically when a `v*` tag is pushed. The release workflow builds
multi-arch images (`linux/amd64`, `linux/arm64`) and pushes to ghcr.io.

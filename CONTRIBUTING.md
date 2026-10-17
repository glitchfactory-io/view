# Development

```sh
go test ./...
CONFIG_FILE=./my-tiles.yaml CACHE_DIR=./cache go run .   # then open http://localhost:8080
docker build -f Containerfile -t view .
```

The config file uses the same format as `config` in the Helm values.

| File | What it is |
| --- | --- |
| `web/index.html` | The page itself, embedded into the binary. |
| `main.go` | The server: serves the page and pushes live updates to open pages. |
| `config.go` | Reads and checks the config, watches it, merges static and route tiles. |
| `discovery.go` | Tiles from labelled HTTPRoutes. |
| `services.go` | The services card. |
| `network.go`, `kube.go` | The network card, and a tiny read-only Kubernetes API client. |
| `icons.go` | Finds each site's icon and caches it. |
| `charts/view/` | The Helm chart. |
| `.github/workflows/` | CI on pull requests; a `v*` tag publishes the image and chart to ghcr.io. |

# Releasing

Bump `version` and `appVersion` in `charts/view/Chart.yaml` in a pull request (`0.2.0` / `"v0.2.0"`),
merge it, then tag that commit:

```sh
git tag -a v0.2.0 -m v0.2.0 && git push origin v0.2.0
```

The release workflow checks that the tag matches the chart, runs the tests, pushes the image
(`ghcr.io/glitchfactory-io/view`, amd64 and arm64), then the chart, then creates a GitHub release.

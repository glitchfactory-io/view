# Development

```sh
go test ./...
CONFIG_FILE=./my-tiles.yaml CACHE_DIR=./cache go run .   # then open http://localhost:8080
docker build -f Containerfile -t view .
```

The config file uses the same format as `config` in the Helm values.

Dependencies are vendored in `vendor/`, so builds work without network access. After adding,
removing or updating a dependency, run `go mod tidy && go mod vendor` and commit `vendor/` with the change.

| File | What it is |
| --- | --- |
| `web/index.html` | The page itself, embedded into the binary. |
| `main.go` | The server: serves the page and pushes live updates to open pages. |
| `config.go` | Reads and checks the config, watches it, merges static and route tiles. |
| `discovery.go` | Tiles from labelled HTTPRoutes. |
| `services.go` | The Platform Services card. |
| `network.go`, `kube.go` | The network card, and a tiny read-only Kubernetes API client. |
| `tooling.go` | The platform footer: recognises the platform's components and their versions. |
| `icons.go` | Finds each site's icon and caches it. |
| `charts/view/` | The Helm chart. |
| `vendor/` | Vendored Go dependencies (`go mod vendor`); don't edit by hand. |
| `.github/workflows/` | CI on pull requests; a `v*` tag publishes the image and chart to ghcr.io. |

# Releasing

Bump `version` and `appVersion` in `charts/view/Chart.yaml` in a pull request (`0.3.0` / `"v0.3.0"`),
merge it, then tag that commit:

```sh
git tag -a v0.3.0 -m v0.3.0 && git push origin v0.3.0
```

The release workflow checks that the tag matches the chart, runs the tests, pushes the image
(`ghcr.io/glitchfactory-io/view`, amd64 and arm64), then the chart, then creates a GitHub release.

The page shows which build is running next to the title, e.g. `view v0.4.0-rc.10 (a1b2c3d)`. Release
builds set it automatically. For any other image build (release candidates, staging), pass it in:

```sh
docker build -f Containerfile -t view:v0.4.0-rc.10 \
  --build-arg VERSION=v0.4.0-rc.10 --build-arg COMMIT=$(git rev-parse --short HEAD) .
```

Without these the image works the same and the page just shows no version.

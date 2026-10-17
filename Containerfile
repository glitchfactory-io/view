# Build stage: compiles the page and server into one small program.
# --platform=$BUILDPLATFORM lets one machine build for both Intel/AMD and ARM (e.g. Raspberry Pi).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /view .

# Run stage: a minimal image (~10 MB) with no shell, running as a non-root user.
FROM gcr.io/distroless/static-debian13:nonroot
LABEL org.opencontainers.image.source="https://github.com/glitchfactory-io/view" \
      org.opencontainers.image.description="A calm, read-only start page for Kubernetes" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /view /view
ENV CONFIG_FILE=/config/tiles.yaml CACHE_DIR=/cache PORT=8080
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/view"]

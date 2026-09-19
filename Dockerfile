# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/booth-catalog ./cmd/catalog

# Distroless static + nonroot (uid/gid 65532): no shell, no package manager; the chart pins the
# same uid.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/booth-catalog /booth-catalog
USER nonroot:nonroot
ENTRYPOINT ["/booth-catalog"]

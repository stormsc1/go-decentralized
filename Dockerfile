FROM golang:1.25 AS build
WORKDIR /src
COPY . .
# Cache modules and build output between builds, so rebuilds (e.g. by Tilt)
# only compile what changed.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /
ENTRYPOINT ["/modularnode"]

# Build context: repository root (docker build -f deploy/migrations.Dockerfile .)
# Built from source instead of the upstream migrate/migrate image: that image is Alpine-based and
# ships a binary with outdated Go modules (quality gate fails on its CVEs). Here the toolchain and
# the vendored dependencies are pinned to CVE-fixed versions and the final image is distroless.
FROM golang:1.27.4@sha256:08bcb99a38f85a265f9a93d1d3323e2440eec333380f3ea26ee0dec9264bb41c AS build
RUN git clone --depth 1 --branch v4.20.1 https://github.com/golang-migrate/migrate.git /src
WORKDIR /src
RUN go get github.com/go-jose/go-jose/v4@v4.1.6 \
		google.golang.org/grpc@v1.83.1 \
		golang.org/x/crypto@v0.58.0 \
		golang.org/x/net@v0.62.0 \
		golang.org/x/text@v0.43.0 \
		github.com/klauspost/compress@v1.18.7 \
	&& CGO_ENABLED=0 go build -trimpath -tags 'postgres file' -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/migrate /migrate
COPY services/database_middleware/migrations /migrations
USER nonroot:nonroot
ENTRYPOINT ["/migrate"]

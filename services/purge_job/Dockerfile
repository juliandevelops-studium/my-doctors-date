# Build context: repository root (docker build -f services/purge_job/Dockerfile .)
FROM golang:1.27@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
COPY libs/ /src/libs/
COPY services/purge_job/ /src/services/purge_job/
WORKDIR /src/services/purge_job
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/purge_job ./cmd/purge_job

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/purge_job /purge_job
USER nonroot:nonroot
ENTRYPOINT ["/purge_job"]

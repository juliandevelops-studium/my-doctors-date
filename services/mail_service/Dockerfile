# Build context: repository root (docker build -f services/mail_service/Dockerfile .)
FROM golang:1.27@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
COPY libs/ /src/libs/
COPY services/mail_service/ /src/services/mail_service/
WORKDIR /src/services/mail_service
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mail_service ./cmd/mail_service

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/mail_service /mail_service
USER nonroot:nonroot
ENTRYPOINT ["/mail_service"]

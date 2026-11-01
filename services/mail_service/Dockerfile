# Build context: repository root (docker build -f services/mail_service/Dockerfile .)
FROM golang:1.26@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 AS build
COPY libs/ /src/libs/
COPY services/mail_service/ /src/services/mail_service/
WORKDIR /src/services/mail_service
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mail_service ./cmd/mail_service

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/mail_service /mail_service
USER nonroot:nonroot
ENTRYPOINT ["/mail_service"]

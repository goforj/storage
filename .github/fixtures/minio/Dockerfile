# Build the upstream security release after its published images were removed.
FROM golang:1.27-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
RUN GOBIN=/out CGO_ENABLED=0 go install github.com/minio/minio@v0.0.0-20251015172955-9e49d5e7a648

FROM scratch
LABEL org.opencontainers.image.source="https://github.com/minio/minio" \
      org.opencontainers.image.revision="9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/minio /minio
ENTRYPOINT ["/minio"]

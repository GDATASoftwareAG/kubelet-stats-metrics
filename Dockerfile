# Build the manager binary
FROM docker.io/library/golang:1.19.7 as builder

WORKDIR /code

COPY . .

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o app main.go

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /code/app .
USER 65532:65532

ENTRYPOINT ["/app"]

LABEL org.opencontainers.image.title="Kubelet stats metrics Docker Image" \
      org.opencontainers.image.description="kubelet-stats-metrics" \
      org.opencontainers.image.url="https://github.com/GDATASoftwareAG/kubelet-stats-metrics" \
      org.opencontainers.image.source="https://github.com/GDATASoftwareAG/kubelet-stats-metrics" \
      org.opencontainers.image.license="MIT"
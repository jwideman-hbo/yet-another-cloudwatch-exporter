FROM golang:1.22 AS builder

WORKDIR /opt/

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

# Use buildx automatic platform args for multi-arch builds
ARG TARGETOS
ARG TARGETARCH
ENV GOOS=${TARGETOS:-linux}
ENV GOARCH=${TARGETARCH}
ENV CGO_ENABLED=0

ARG VERSION
RUN go build -v -ldflags "-X main.version=$VERSION" -o yace ./cmd/yace

FROM alpine:3.19.1

EXPOSE 5000
ENTRYPOINT ["yace"]
CMD ["--config.file=/tmp/config.yml"]
RUN addgroup -g 1000 exporter && \
    adduser -u 1000 -D -G exporter exporter -h /exporter

WORKDIR /exporter/


RUN apk --no-cache add ca-certificates
COPY --from=builder /opt/yace /usr/local/bin/yace
USER exporter

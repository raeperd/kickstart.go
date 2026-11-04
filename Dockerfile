FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder
ARG VERSION=local
ARG TARGETOS TARGETARCH

WORKDIR /src

COPY ./go.mod ./go.sum ./
COPY ./Makefile .

RUN go mod download

COPY . .

RUN make build TARGET_EXEC=app CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH VERSION=${VERSION}

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /src/app /bin/app

USER 65534:65534
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["app"]

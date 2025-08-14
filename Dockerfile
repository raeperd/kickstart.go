FROM golang:1.24-bullseye as builder
ARG VERSION=local

WORKDIR /src

COPY ./go.mod ./go.sum ./
COPY ./Makefile .

RUN make download

COPY . .

RUN make build TARGET_EXEC=app CGO_ENABLED=0 VERSION=${VERSION}

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /src/app /bin/app

EXPOSE 8080
ENTRYPOINT ["app"]
CMD ["--port=8080"]

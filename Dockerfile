FROM golang:1.22-bullseye as builder
ARG VERSION=local

WORKDIR /src
COPY . /src
RUN make download
RUN make build TARGET_EXEC=app CGO_ENABLED=0 VERSION=${VERSION}

FROM scratch
COPY --from=builder /src/app /bin/app
ENTRYPOINT ["/bin/app"]
CMD ["--port=8080"]

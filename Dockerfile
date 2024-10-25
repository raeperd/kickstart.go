FROM golang:1.22-bullseye as builder
ARG VERSION=local

WORKDIR /src

COPY ./go.mod ./go.sum ./
COPY ./Makefile .

RUN make download

COPY . .

RUN make build TARGET_EXEC=app CGO_ENABLED=0 VERSION=${VERSION}

FROM gcr.io/distroless/static-debian12

COPY --from=builder /src/app /bin/app

EXPOSE 8080
ENTRYPOINT ["app"]
CMD ["--port=8080"]

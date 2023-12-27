FROM golang:1.21-bullseye as builder
WORKDIR /src
COPY . /src
RUN make compile TARGET_EXEC=app CGO_ENABLED=0 

FROM scratch
COPY --from=builder /src/app /bin/app
ENTRYPOINT ["/bin/app"]
CMD ["--port=8080"]
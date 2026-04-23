ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}

WORKDIR /app

COPY . .

RUN make build

ENTRYPOINT ["/app/clod"]

FROM alpine:3.14 as source-tree

RUN apk add --no-cache git

COPY . /src

RUN git init \
  && git clean -fX . \
  && rm -rf .git \
  && rm -r /src/build/dockerfile

FROM golang:1.21.4 as build-kuscia-bootstrap

WORKDIR /src

COPY --from=source-tree /src/go.mod /src/go.sum ./

RUN go mod download

COPY --from=source-tree /src/ ./

RUN go build -o build/apps/kuscia/kuscia-bootstrap ./cmd/bootstrap

FROM tonywu6/kuscia:linux-arm64

COPY --from=build-kuscia-bootstrap /src/build/apps/kuscia/kuscia-bootstrap /home/kuscia/bin/kuscia-bootstrap

ENTRYPOINT [ "tini", "--" ]

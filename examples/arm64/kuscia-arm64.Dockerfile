FROM alpine:3.14 as source-tree

RUN apk add --no-cache git

COPY . /src

RUN git init \
  && git clean -fX . \
  && rm -rf .git \
  && rm -r /src/build/dockerfile

FROM golang:1.22.1 as build-kuscia

WORKDIR /src

COPY --from=source-tree \
  /src/go.work \
  /src/go.work.sum \
  /src/go.mod \
  /src/go.sum \
  ./
COPY --from=source-tree \
  /src/examples/bootstrap/go.mod \
  /src/examples/bootstrap/go.sum \
  /src/examples/bootstrap/

RUN go mod download

COPY --from=source-tree /src/ ./

RUN go build -o build/apps/kuscia/kuscia ./cmd/kuscia
RUN go build -o build/apps/kuscia/kuscia-bootstrap ./examples/bootstrap

FROM tonywu6/kuscia-envoy:linux-arm64 as image-kuscia-envoy

FROM tonywu6/proot:linux-arm64 as image-proot

FROM rancher/k3s:v1.26.14-k3s1-arm64 as image-k3s

FROM prom/node-exporter:v1.7.0 as image-node-exporter

FROM debian:bookworm

ENV TZ=Asia/Shanghai

ARG ROOT_DIR="/home/kuscia"

RUN apt-get update
RUN apt-get install -y \
  openssl curl net-tools jq logrotate iproute2 \
  && apt-get clean

WORKDIR /home/kuscia

RUN mkdir -p bin

COPY --from=image-proot /src/src/proot /home/kuscia/bin/proot
COPY --from=image-k3s /bin/aux /bin/aux
COPY --from=image-k3s /bin/k3s \
  /bin/containerd \
  /bin/containerd-shim-runc-v2 \
  /bin/runc \
  /bin/cni \
  bin/

ARG TINI_VERSION=v0.19.0

ADD https://github.com/krallin/tini/releases/download/${TINI_VERSION}/tini-arm64 \
  bin/tini
RUN chmod +x bin/tini

COPY --from=image-node-exporter /bin/node_exporter bin/

RUN cd bin \
  && ln -s k3s crictl \
  && ln -s k3s ctr \
  && ln -s k3s kubectl \
  && ln -s cni bridge \
  && ln -s cni flannel \
  && ln -s cni host-local \
  && ln -s cni loopback \
  && ln -s cni portmap

COPY --from=build-kuscia /src/build/apps/kuscia/kuscia bin/
COPY --from=build-kuscia /src/build/apps/kuscia/kuscia-bootstrap bin/
COPY --from=image-kuscia-envoy /src/bazel-bin/envoy bin/

COPY crds/v1alpha1 crds/v1alpha1
COPY etc etc
COPY testdata var/storage/data
COPY scripts scripts

COPY build/pause/pause-linux-arm64.tar pause/pause.tar

ENV PATH="/home/kuscia/bin:/bin/aux:${PATH}"

ENTRYPOINT [ "tini", "--" ]

FROM ubuntu:jammy as build-kuscia-envoy

RUN apt-get update \
  && apt-get install -y ca-certificates gpg wget \
  && wget -O - https://apt.kitware.com/keys/kitware-archive-latest.asc 2>/dev/null | gpg --dearmor - | tee /usr/share/keyrings/kitware-archive-keyring.gpg >/dev/null \
  && echo 'deb [signed-by=/usr/share/keyrings/kitware-archive-keyring.gpg] https://apt.kitware.com/ubuntu/ jammy main' > /etc/apt/sources.list.d/kitware.list \
  && apt-get update \
  && apt-get install -y \
  git curl build-essential cmake ninja-build python3

RUN curl -fsSL https://github.com/bazelbuild/bazelisk/releases/download/v1.19.0/bazelisk-linux-arm64 \
  -o /usr/local/bin/bazel \
  && chmod u+x /usr/local/bin/bazel

ARG KUSCIA_ENVOY_VERSION=v0.3.0b0

RUN git clone https://github.com/secretflow/kuscia-envoy /src \
  --depth 1 \
  --branch ${KUSCIA_ENVOY_VERSION} \
  --recurse-submodules \
  --shallow-submodules

WORKDIR /src

RUN bazel fetch //...
RUN bazel build -c opt //:envoy \
  --verbose_failures \
  --strip=always \
  --@envoy//source/extensions/wasm_runtime/v8:enabled=false

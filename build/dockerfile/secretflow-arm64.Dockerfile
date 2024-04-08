FROM quay.io/pypa/manylinux_2_28_aarch64 as build

RUN yum install -y ninja-build perl-IPC-Cmd

RUN curl -fsSL https://github.com/bazelbuild/bazelisk/releases/download/v1.19.0/bazelisk-linux-arm64 \
  -o /usr/local/bin/bazel \
  && chmod u+x /usr/local/bin/bazel

ARG SECRETFLOW_VERSION=main

RUN git clone https://github.com/secretflow/secretflow /src \
  --depth 1 \
  --branch ${SECRETFLOW_VERSION}

WORKDIR /src

ARG PYTHON_TAG=cp310-cp310
ENV PATH=/opt/python/${PYTHON_TAG}/bin:$PATH

RUN python3 setup.py bdist_wheel
RUN auditwheel repair dist/*.whl --wheel-dir /tmp/wheelhouse

FROM mambaorg/micromamba:bookworm

USER root

RUN apt-get update \
  && apt-get install -y \
  libgomp1 \
  && apt-get clean

USER mambauser

ENV MAMBA_ROOT_PREFIX=/home/mambauser/.micromamba

WORKDIR /home/mambauser

COPY --from=build /tmp/wheelhouse wheelhouse

ARG PYTHON_VERSION=3.10

RUN micromamba install -y -n base -c conda-forge \
  python=${PYTHON_VERSION} \
  pip

RUN micromamba run -n base python -m pip install \
  --find-links /home/mambauser/wheelhouse \
  "secretflow>=1.0.0.dev0"

RUN micromamba env export -n base > /home/mambauser/environment.yml

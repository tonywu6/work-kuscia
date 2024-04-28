FROM gcc:7.4.0 as build-proot

ARG PROOT_VERSION=v5.4.0

RUN apt-get update
RUN apt-get install -y \
  git clang-tools-6.0 curl docutils-common gdb lcov libarchive-dev libtalloc-dev strace swig uthash-dev xsltproc

RUN git clone https://github.com/proot-me/proot /src \
  --depth 1 \
  --branch ${PROOT_VERSION}

WORKDIR /src

RUN LDFLAGS="${LDFLAGS} -static" make -C src proot GIT=false

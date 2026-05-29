### BUILDER STAGE
#
#
FROM golang:1.26-alpine3.23 AS builder

# Do not remove `git` here, it is required for getting runner version when executing `make build`
RUN apk add --no-cache make git

ARG GOPROXY
ENV GOPROXY=${GOPROXY:-}

COPY . /opt/src/runner
WORKDIR /opt/src/runner

RUN make clean && make build

### DIND VARIANT
#
#
FROM docker:29.5.2-dind AS dind

ARG VERSION=dev

LABEL org.opencontainers.image.source="https://gitea.com/gitea/runner"
LABEL org.opencontainers.image.version="${VERSION}"

RUN apk add --no-cache s6 bash git tzdata

COPY --from=builder /opt/src/runner/gitea-runner /usr/local/bin/gitea-runner
COPY scripts/run.sh /usr/local/bin/run.sh
COPY scripts/s6 /etc/s6

VOLUME /data

ENTRYPOINT ["s6-svscan","/etc/s6"]

### DIND-ROOTLESS VARIANT
#
#
FROM docker:29.5.2-dind-rootless AS dind-rootless

ARG VERSION=dev

LABEL org.opencontainers.image.source="https://gitea.com/gitea/runner"
LABEL org.opencontainers.image.version="${VERSION}"

USER root
RUN apk add --no-cache s6 bash git tzdata

COPY --from=builder /opt/src/runner/gitea-runner /usr/local/bin/gitea-runner
COPY scripts/run.sh /usr/local/bin/run.sh
COPY scripts/s6 /etc/s6

VOLUME /data

RUN mkdir -p /data && chown -R rootless:rootless /etc/s6 /data

ENV DOCKER_HOST=unix:///run/user/1000/docker.sock

USER rootless
ENTRYPOINT ["s6-svscan","/etc/s6"]

### BASIC VARIANT
#
#
FROM alpine:3.23 AS basic

ARG VERSION=dev

LABEL org.opencontainers.image.source="https://gitea.com/gitea/runner"
LABEL org.opencontainers.image.version="${VERSION}"

RUN apk add --no-cache tini bash git tzdata

COPY --from=builder /opt/src/runner/gitea-runner /usr/local/bin/gitea-runner
COPY scripts/run.sh /usr/local/bin/run.sh

VOLUME /data

ENTRYPOINT ["/sbin/tini","--","run.sh"]

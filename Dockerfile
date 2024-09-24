FROM golang:1.23-alpine AS builder
# Do not remove `git` here, it is required for getting runner version when executing `make build`
RUN apk add --no-cache make git

COPY . /opt/src/act_runner
WORKDIR /opt/src/act_runner

RUN make clean && make build

FROM alpine
RUN apk add --no-cache git bash tini

COPY --from=builder /opt/src/act_runner/act_runner /usr/local/bin/act_runner
COPY scripts/run.sh /opt/act/run.sh

ENTRYPOINT ["/sbin/tini","--","/opt/act/run.sh"]

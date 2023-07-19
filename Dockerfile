FROM golang:1.20.5-alpine3.18 as builder
# Do not remove `git` here, it is required for getting runner version when executing `make build`
RUN apk add --no-cache make git

COPY . /opt/src/act_runner
WORKDIR /opt/src/act_runner

RUN make clean && make build

FROM alpine:3.18
RUN apk add --no-cache git bash tini

COPY --from=builder /opt/src/act_runner/act_runner /usr/local/bin/act_runner
COPY scripts/run.sh /opt/act/run.sh

ENTRYPOINT ["/sbin/tini","--","/opt/act/run.sh"]

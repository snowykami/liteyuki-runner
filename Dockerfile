FROM golang:1.20-alpine3.17 as builder
RUN apk add --no-cache make=4.3-r1

COPY . /opt/src/act_runner
WORKDIR /opt/src/act_runner

RUN make clean && make build

FROM alpine:3.17
RUN apk add --no-cache \
  git=2.38.5-r0 bash=5.2.15-r0 \
  && rm -rf /var/cache/apk/*

COPY --from=builder /opt/src/act_runner/act_runner /usr/local/bin/act_runner
COPY run.sh /opt/act/run.sh

ENTRYPOINT ["/opt/act/run.sh"]

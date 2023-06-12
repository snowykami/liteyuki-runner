#!/usr/bin/env bash

# wait for docker daemon
while ! nc -z localhost 2376 </dev/null; do
  echo 'waiting for docker daemon...'
  sleep 5
done

. /opt/act/run.sh

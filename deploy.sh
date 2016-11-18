#!/bin/bash

APP_NAME=beekeeper-updater-swarm
LOCAL_IMAGE_NAME=local/$APP_NAME
REMOTE_IMAGE_NAME=octoblu/$APP_NAME

panic() {
  local message=$1
  echo $message
  exit 1
}

main() {
  local tag="$(git describe --tags --exact --match 'v*.*.*')"
  if [ "$?" != "0" ]; then
    panic 'not a tagged commit'
  fi
  ./build.sh || panic 'build failed'
  echo "building tag $tag"
  docker tag $LOCAL_IMAGE_NAME:latest $REMOTE_IMAGE_NAME:$tag
  docker push $REMOTE_IMAGE_NAME:$tag
}

main

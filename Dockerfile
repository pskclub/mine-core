FROM golang:1.18.0-alpine3.15

RUN apk update && apk upgrade && \
apk add --no-cache bash git openssh
RUN apk add build-base
RUN git config --global url."https://mine-core-deploy:Zr2TnbF6X9oMLAQKxvvX@gitlab.finema.co".insteadOf "https://gitlab.finema.co"

WORKDIR /app

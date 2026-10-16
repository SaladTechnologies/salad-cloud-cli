# syntax=docker/dockerfile:1

# $ docker run --rm -e SALAD_API_KEY ghcr.io/saladtechnologies/saladcloud gpus list
FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETPLATFORM

COPY ${TARGETPLATFORM}/saladcloud /usr/local/bin/saladcloud

ENTRYPOINT ["/usr/local/bin/saladcloud"]

# syntax=docker/dockerfile:1

# $ docker run --rm -e SALAD_API_KEY ghcr.io/saladtechnologies/saladcloud gpus list
# Pinned by digest; Dependabot moves it.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

ARG TARGETPLATFORM

COPY ${TARGETPLATFORM}/saladcloud /usr/local/bin/saladcloud

ENTRYPOINT ["/usr/local/bin/saladcloud"]

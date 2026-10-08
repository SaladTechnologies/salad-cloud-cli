# The container image goreleaser builds for each release: the static binary
# on a distroless base. Run it with your key in the environment:
#   docker run --rm -e SALAD_API_KEY ghcr.io/saladtechnologies/saladcloud gpus list
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/saladcloud /usr/local/bin/saladcloud
ENTRYPOINT ["/usr/local/bin/saladcloud"]

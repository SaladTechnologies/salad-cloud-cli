.PHONY: build test test-live vet fmt

build:
	bash scripts/build.sh

test:
	go test ./...

# Live smoke suite against the real API. Needs a built binary plus
# SALAD_API_KEY and SALAD_ORGANIZATION (SALAD_PROJECT optional); fails
# without them, so a pass always means it ran. Optional:
# SALAD_CLI_TEST_REQUIRE_SSH=1 fails the ssh test instead of skipping it on
# a publickey denial (set it for the pre-tag run);
# SALAD_AI_GATEWAY_API_KEY adds the AI Gateway smoke test;
# SALAD_CLI_TEST_BIN points at a binary other than dist/;
# SALAD_CLI_TEST_PHRASE is a phrase the audio contains;
# SALAD_CLI_TEST_AUDIO_FOREIGN is a short single-track non-English clip
# (language_code and audio_stream_index test).
# Set SALAD_CLI_TEST_AUDIO to a short wav
# to include transcription. -short omits the two tests that run a workload.
test-live: build
	go test -tags live ./internal/livetest/ -timeout 30m -v

vet:
	go vet ./...

fmt:
	gofmt -w .

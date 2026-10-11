.PHONY: build dashboard-build dashboard-ci distribution test-libghostty test-analytics-s3 test-filesystem-s3 test-litestream

# Normal Go builds intentionally do not require Node or generated dashboard
# assets. Distribution builds embed the generated frontend.
build:
	go build ./...

dashboard-build:
	npm --prefix dashboard ci
	npm --prefix dashboard run build

dashboard-ci:
	npm --prefix dashboard ci
	npm --prefix dashboard run check
	npm --prefix dashboard test
	npm --prefix dashboard run build

distribution: dashboard-build
	go build -trimpath -o openlore ./cmd/openlore

test-libghostty:
	./scripts/test-libghostty.sh

# Requires Docker; starts and removes an isolated LocalStack S3 container.
test-analytics-s3:
	go test -race -tags=integration ./internal/analytics -run '^TestS3Analytics$$' -count=1 -timeout=15m

test-filesystem-s3:
	go test -race -tags=integration ./pkg/openlore -run '^TestS3Filesystem$$' -count=1 -timeout=15m

test-litestream:
	bash ./scripts/test-litestream.sh

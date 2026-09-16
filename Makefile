.PHONY: build dashboard-build dashboard-ci distribution test-libghostty

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

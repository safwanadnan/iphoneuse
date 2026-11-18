VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet helper-project helper-check install

build:
	go build -ldflags "$(LDFLAGS)" -o bin/iphoneuse ./cmd/iphoneuse

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/iphoneuse

test:
	go test ./...

vet:
	go vet ./...

helper-project:
	cd helper && xcodegen generate

helper-check:
	xcodebuild build-for-testing -project helper/iPhoneUseHelper.xcodeproj -scheme iPhoneUseRunner \
		-destination 'generic/platform=iOS Simulator' -derivedDataPath /tmp/iphoneuse-ci CODE_SIGNING_ALLOWED=NO

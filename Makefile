VERSION ?= 1.0.0
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: test vet build build-all validate-plugins clean

vet:
	CGO_ENABLED=0 go vet ./...

test:
	CGO_ENABLED=0 go test -v -count=1 ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/tradebox ./cmd/tradebox

build-all:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/tradebox-windows-amd64.exe ./cmd/tradebox
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/tradebox-linux-amd64 ./cmd/tradebox
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/tradebox-darwin-arm64 ./cmd/tradebox
	cd dist && sha256sum tradebox-windows-amd64.exe tradebox-linux-amd64 tradebox-darwin-arm64 > SHA256SUMS.txt

validate-plugins:
	node scripts/validate-plugins.mjs

clean:
	rm -rf dist

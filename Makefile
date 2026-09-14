IMAGE ?= ghcr.io/initialed85/longhorn-nfs-gateway:dev

.PHONY: fmt vet test build manifests kustomize docker-build

fmt:
	gofmt -w api cmd internal

vet:
	go vet ./...

test:
	go test ./...

build:
	go build ./cmd/controller

manifests:
	kubectl kustomize deploy >/dev/null

kustomize:
	kubectl kustomize config/samples/poc/rwo >/dev/null
	kubectl kustomize config/samples/poc/rwx >/dev/null

docker-build:
	docker build --tag $(IMAGE) .

.PHONY: all build test generate manifests install deploy docker-build docker-push clean

IMG ?= mssql-k8s-operator:latest

all: build

build:
	go build -o bin/manager ./cmd/main.go

test:
	go test ./... -count=1

generate:
	go generate ./...

manifests:
	@echo "TODO: generate CRD manifests with controller-gen"

install:
	@echo "TODO: install CRDs via kubectl apply"

deploy:
	@echo "TODO: deploy operator via kustomize"

docker-build:
	docker build -t $(IMG) .

docker-push:
	docker push $(IMG)

clean:
	rm -rf bin/

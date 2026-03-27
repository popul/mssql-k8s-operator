.PHONY: all build test test-integration test-fullstack test-e2e test-all generate manifests install deploy docker-build docker-push clean lint

IMG ?= mssql-k8s-operator:latest

all: build

build:
	go build -o bin/manager ./cmd/main.go

test:
	go test ./... -count=1 -race

test-integration:
	go test -tags=integration ./internal/sql/... -count=1 -v -timeout=300s

test-fullstack:
	go test -tags=fullstack ./... -count=1 -v -timeout=600s

test-e2e:
	go test -tags=e2e ./test/e2e/... -count=1 -v -timeout=900s

test-all: test test-integration

CONTROLLER_GEN ?= $(shell go env GOPATH)/bin/controller-gen

generate:
	$(CONTROLLER_GEN) object paths="./api/..."

manifests:
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:dir=charts/mssql-operator/crds
	$(CONTROLLER_GEN) rbac:roleName=mssql-operator-manager paths="./internal/controller/..." output:rbac:dir=config/rbac

install:
	kubectl apply -f charts/mssql-operator/crds/

deploy:
	helm upgrade --install mssql-operator charts/mssql-operator/ --namespace mssql-operator-system --create-namespace

docker-build:
	docker build -t $(IMG) .

docker-push:
	docker push $(IMG)

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/

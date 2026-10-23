.PHONY: help certs helm-certs proto build test kind-up deploy deploy-test deploy-test-job wait-test-job integration-test integration-test-ci clean

SHELL := /bin/bash
GO := go
BIN := bin/replica-manager
MODULE := replica-manager
NAMESPACE := default
KIND_CLUSTER := replica-manager
IMAGE := $(shell echo $(MODULE) | tr '/' '-')
HELM_RELEASE := replica-manager
HELM_DIR := helm
CERT_DIR := certs

# Copy current certs into the helm chart dir so the chart can package them
# into the Secret template (charts can only .Files.Get inside their own dir).
helm-certs: certs ## Stage certs into the helm chart for Secret creation
	@mkdir -p $(HELM_DIR)/certs
	@cp $(CERT_DIR)/ca.pem $(CERT_DIR)/ca-private.pem \
		$(CERT_DIR)/server-public.pem $(CERT_DIR)/server-private.pem \
		$(CERT_DIR)/client-public.pem $(CERT_DIR)/client-private.pem \
		$(HELM_DIR)/certs/

help: ## Print available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

certs: ## Generate CA, server, and client certificates for mTLS (ECDSA)
	@./scripts/generate-certs.sh

docker-build: ## Build Docker image
	docker build -t $(IMAGE):latest .

proto: ## Compile protobuf definitions to Go stubs
	protoc -I=api/replicamanager/v1 \
		--go_out=api/replicamanager/v1 \
		--go_opt=paths=source_relative \
		--go-grpc_out=api/replicamanager/v1 \
		--go-grpc_opt=paths=source_relative \
		api/replicamanager/v1/service.proto

build: certs ## Build Go binary and Docker image
	$(GO) build -o $(BIN) github.com/mochacat/replica-manager/cmd/server
	docker build -t $(IMAGE):latest .

test: ## Run unit tests
	@$(GO) test ./...

kind-up: ## Create a KIND cluster (no-op if already exists)
	@kind create cluster --name $(KIND_CLUSTER) 2>/dev/null || true

deploy: kind-up build helm-certs ## Deploy Helm chart to cluster (dev certs)
	@kind load docker-image $(IMAGE):latest --name $(KIND_CLUSTER) 2>/dev/null || true
	-kubectl delete deployment --namespace $(NAMESPACE) $(HELM_RELEASE) 2>/dev/null || true
	helm upgrade --install $(HELM_RELEASE) $(HELM_DIR) --namespace $(NAMESPACE) --create-namespace --set certs.create=true

TEST_IMAGE := $(IMAGE)-test

build-test: ## Build integration test image
	docker build -t $(TEST_IMAGE):latest -f test/Dockerfile.test .

deploy-test: build helm-certs ## Deploy to ephemeral test namespace (dev certs)
	@kind load docker-image $(IMAGE):latest --name $(KIND_CLUSTER) 2>/dev/null || true
	helm upgrade --install $(HELM_RELEASE) $(HELM_DIR) --namespace replica-manager-test --create-namespace --set namespace=replica-manager-test --set certs.create=true

deploy-test-job: build-test helm-certs ## Deploy integration test Job + fixtures
	@kind load docker-image $(TEST_IMAGE):latest --name $(KIND_CLUSTER) 2>/dev/null || true
	-kubectl delete job --namespace replica-manager-test replica-manager-test 2>/dev/null || true
	kubectl apply --namespace replica-manager-test -f test/test-deployment.yaml
	# Deploy fixtures the integration test targets (SetReplicas/GetReplicaCount)
	kubectl apply --namespace replica-manager-test -f test/fixtures/podinfo-deployment.yaml
	# Unmanaged fixture (no replicamanager/replicas annotation) used to verify the
	# reconciler leaves non-managed Deployments alone.
	kubectl apply --namespace replica-manager-test -f test/fixtures/unmanaged-deployment.yaml

# Poll the test Job pod until it terminates (exit code 0 or non-zero).
# Uses the job-name label that K8s automatically sets on Job pods.
wait-test-job:
	$(eval POD := $(shell kubectl get pods --namespace replica-manager-test -l job-name=replica-manager-test -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo ""))
	@echo "Waiting for test job pod..."
	@for i in $$(seq 1 90); do \
		POD=$$(kubectl get pods --namespace replica-manager-test -l job-name=replica-manager-test -o jsonpath='{.items[0].metadata.name}' 2>/dev/null); \
		if [ -z "$$POD" ]; then \
			sleep 5; continue; \
		fi; \
		ACTUAL_EXIT=$$(kubectl get pods --namespace replica-manager-test "$$POD" -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}' 2>/dev/null); \
		if [ -n "$$ACTUAL_EXIT" ]; then \
			echo "--- TEST LOGS ---"; \
			kubectl logs --namespace replica-manager-test "$$POD" 2>/dev/null; \
			echo "--- END LOGS ---"; \
			exit $$([ "$$ACTUAL_EXIT" = "0" ] && echo 0 || echo 1); \
		fi; \
		sleep 5; \
	done; \
	echo "Timed out waiting for test job"; exit 1

integration-test: ## Run integration tests (local: creates cluster, tears down)
	@$(MAKE) kind-up
	@$(MAKE) deploy-test
	@$(MAKE) deploy-test-job
	@$(MAKE) wait-test-job; TEST_EXIT=$$?; \
		helm delete --namespace replica-manager-test $(HELM_RELEASE)-test 2>/dev/null || true; \
		helm delete --namespace replica-manager-test $(HELM_RELEASE) 2>/dev/null || true; \
		kubectl delete namespace replica-manager-test 2>/dev/null || true; \
		exit $$TEST_EXIT

integration-test-ci: ## Run integration tests in CI (cluster managed by GitHub Actions)
	@$(MAKE) kind-up build
	@$(MAKE) deploy-test
	@$(MAKE) deploy-test-job
	@$(MAKE) wait-test-job

clean: ## Tear down cluster, remove binaries, clean certs and test artifacts
	@kind delete cluster --name $(KIND_CLUSTER) 2>/dev/null || true
	-docker rmi $(IMAGE):latest 2>/dev/null || true
	-docker rmi $(TEST_IMAGE):latest 2>/dev/null || true
	-rm -rf $(BIN) certs/ helm/certs/
	-helm delete --namespace replica-manager-test $(HELM_RELEASE) 2>/dev/null || true
	-kubectl delete namespace replica-manager-test 2>/dev/null || true

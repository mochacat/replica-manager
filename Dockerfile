# Builder stage
FROM golang:1.26-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o /app/service github.com/mochacat/replica-manager/cmd/server

FROM gcr.io/distroless/base-debian12:nonroot

COPY --from=builder --chown=65532:65532 /app/service /app/service

# The replica policy is baked into the image (see internal/policy/policy.rego).
# Enable it by default for the prod image so bounds are tamper-resistant out of
# the box; operators may override the container env to explicitly disable.
ENV REPLICA_POLICY_ENABLED=true

EXPOSE 8443

ENTRYPOINT ["/app/service"]
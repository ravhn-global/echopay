.PHONY: run build test lint tidy sqlc migrate-up migrate-down

BINARY := bin/server
PKG := github.com/ravhn/echoapp-backend

run:
	go run ./cmd/server

build:
	go build -o $(BINARY) ./cmd/server

test:
	go test ./... -race -count=1

lint:
	go vet ./...

tidy:
	go mod tidy

sqlc:
	sqlc generate

migrate-up:
	migrate -path migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path migrations -database "$(DATABASE_URL)" down 1

migrate-create:
	migrate create -ext sql -dir migrations -seq $(name)

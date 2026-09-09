.PHONY: DownLatestLoadDockerNostrRelay StartNostrRelay ShowDockerLogs PublishNostrNote CheckUuidDuplictes RunUnitTests RunUnitIntegrationTests

DWARF_HOST ?= 192.168.50.136
DWARF_CAMERA ?= wide
DWARF_DEBUG_WS ?= 1

DownLatestLoadDockerNostrRelay:
	docker pull ghcr.io/mattn/nostr-relay:latest

StartNostrRelay:
	docker compose up -d

ShowDockerLogs:
	docker compose logs -f

PublishNostrNote:
	go run ./cmd/nostrpublish -content "tracking note"

CheckUuidDuplictes:
	python3 list_go_guids.py

RunUnitTests:
	go test ./...

RunUnitIntegrationTests:
	@printf 'Running live DWARF integration tests\n  Host: %s\n  Camera: %s\n  WebSocket debug: %s\n\n' '$(DWARF_HOST)' '$(DWARF_CAMERA)' '$(DWARF_DEBUG_WS)'
	RUN_DWARF_INTEGRATION=1 DWARF_HOST=$(DWARF_HOST) DWARF_CAMERA=$(DWARF_CAMERA) DWARF_DEBUG_WS=$(DWARF_DEBUG_WS) go test -v -count=1 -timeout=3m -run '^TestDwarfIntegration' .

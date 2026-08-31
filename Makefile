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

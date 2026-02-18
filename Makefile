.PHONY: build build-arm deploy logs status clean

# Build for Linux amd64
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o taskwhisperer .

# Build for Linux ARM64 (Oracle Free Tier)
build-arm:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o taskwhisperer .

# Deploy to VPS (set VPS_USER and VPS_HOST)
VPS_USER ?= user
VPS_HOST ?= your-vps
VPS_PATH ?= /home/$(VPS_USER)/taskwhisperer

deploy: build
	scp taskwhisperer $(VPS_USER)@$(VPS_HOST):$(VPS_PATH)/
	ssh $(VPS_USER)@$(VPS_HOST) "sudo systemctl restart taskwhisperer"

# View live logs from VPS
logs:
	ssh $(VPS_USER)@$(VPS_HOST) "journalctl -u taskwhisperer -f"

# Check service status on VPS
status:
	ssh $(VPS_USER)@$(VPS_HOST) "sudo systemctl status taskwhisperer"

# Run locally
run:
	go run .

# Clean build artifacts
clean:
	rm -f taskwhisperer

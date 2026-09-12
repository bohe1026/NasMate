GO ?= go

.PHONY: backend-test backend-build frontend-build build

backend-test:
	cd backend && $(GO) test ./...

backend-build:
	mkdir -p rootfs_amd64/bin rootfs_arm64/bin
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o ../rootfs_amd64/bin/ugreen-ai-backend .
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o ../rootfs_arm64/bin/ugreen-ai-backend .

frontend-build:
	npm run build
	rm -rf rootfs_common/www
	cp -R dist rootfs_common/www

build: frontend-build backend-build

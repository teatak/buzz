IMAGE ?= teatak/buzz
TAG ?= latest
PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: dev admin-build admin-dev docker-build docker-push docker-publish version-patch version-minor version-major tag release release-patch release-minor release-major

dev:
	@test -f config.yaml || cp config.example.yaml config.yaml
	docker compose -f docker-compose.dev.yml up -d postgres redis
	go run ./cmd/buzz -config config.yaml & \
	$(MAKE) admin-dev

admin-build:
	cd admin && pnpm install --frozen-lockfile --config.confirm-modules-purge=false && pnpm build

admin-dev:
	cd admin && pnpm install --config.confirm-modules-purge=false && pnpm dev

docker-build:
	docker build -t $(IMAGE):$(TAG) .

docker-push:
	docker push $(IMAGE):$(TAG)

docker-publish:
	docker buildx build --platform $(PLATFORMS) -t $(IMAGE):$(TAG) -t $(IMAGE):$$(cat VERSION) --push .

version-patch:
	@awk -F. '{ printf "%d.%d.%d\n", $$1, $$2, $$3 + 1 }' VERSION > VERSION.tmp
	@mv VERSION.tmp VERSION
	@cat VERSION

version-minor:
	@awk -F. '{ printf "%d.%d.0\n", $$1, $$2 + 1 }' VERSION > VERSION.tmp
	@mv VERSION.tmp VERSION
	@cat VERSION

version-major:
	@awk -F. '{ printf "%d.0.0\n", $$1 + 1 }' VERSION > VERSION.tmp
	@mv VERSION.tmp VERSION
	@cat VERSION

tag:
	@v=$$(cat VERSION); \
	if git rev-parse "v$$v" >/dev/null 2>&1; then \
		echo "Tag v$$v already exists"; exit 1; \
	fi; \
	if ! git diff --quiet VERSION; then \
		git add VERSION && \
		git commit -m "chore: bump version to $$v"; \
	fi; \
	git tag -a "v$$v" -m "Release v$$v" && \
	git push origin HEAD "v$$v" && \
	echo "Successfully created and pushed tag v$$v"

release: release-patch

release-patch:
	IMAGE=$(IMAGE) PLATFORMS=$(PLATFORMS) ./scripts/release.sh patch

release-minor:
	IMAGE=$(IMAGE) PLATFORMS=$(PLATFORMS) ./scripts/release.sh minor

release-major:
	IMAGE=$(IMAGE) PLATFORMS=$(PLATFORMS) ./scripts/release.sh major

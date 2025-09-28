.PHONY: build
build:
	npx tsc --outDir ./dist/cjs --module commonjs
	npx tsc --outDir ./dist/esm --module nodenext

.PHONY: proto
proto:
	protoc \
		--plugin=./node_modules/.bin/protoc-gen-ts_proto \
		--proto_path=../../pubsub \
		--ts_proto_out=. \
		pubsub.proto

.PHONY: clean
clean:
	rm -rf dist subtrace-cloudflare-*.tgz

.PHONY: pack
pack:
	npm pack

.PHONY: publish
publish:
	npm publish

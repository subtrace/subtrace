.PHONY: build
build:
	./node_modules/typescript/bin/tsc --project tsconfig.esm.json
	./node_modules/typescript/bin/tsc --project tsconfig.cjs.json

.PHONY: proto
proto:
	protoc \
		--plugin=./node_modules/.bin/protoc-gen-ts_proto \
		--proto_path=../../pubsub \
		--ts_proto_out=. \
		pubsub.proto

.PHONY: clean
clean:
	rm -rf dist subtrace-next-*.tgz

.PHONY: pack
pack:
	npm pack

.PHONY: publish
publish:
	npm publish

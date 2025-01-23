.PHONY: download
download:
	rm subtrace/subtrace-linux-*
	bash -c 'for os in linux; do for arch in amd64 arm64; do curl -L "https://subtrace.dev/download/latest/$$os/$$arch/subtrace" -o subtrace/subtrace-$$os-$$arch; chmod +x subtrace/subtrace-$$os-$$arch; done; done'
	sha256sum subtrace/subtrace-linux-*

.PHONY: clean
clean:
	rm -rf build node_modules

.PHONY: build
build:
	npm install

.PHONY: publish
publish:
	version=$(printf "0.0.%03d" "$(git log --oneline | wc -l)")
	npm version $version
	npm publish --access public

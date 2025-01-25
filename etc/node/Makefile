.PHONY: pack
pack:
	cp package.json /tmp/package.json.bak
	npm version $(shell printf "0.0.%03d" "$(shell git log --oneline | wc -l)")
	npm pack
	mv /tmp/package.json.bak package.json

.PHONY: clean
clean:
	rm -rf *.tgz subtrace-* build

.PHONY: publish
publish:
	cp package.json /tmp/package.json.bak
	npm version $(shell printf "0.0.%03d" "$(shell git log --oneline | wc -l)")
	npm publish
	mv /tmp/package.json.bak package.json

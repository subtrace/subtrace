.PHONY: default
default: build

.PHONY: build
build:
	python3 build.py | gzip >bundle/devtools.js.gz

.PHONY: fetch
fetch:
	git clone https://chromium.googlesource.com/chromium/tools/depot_tools.git
	PATH="${PWD}/depot_tools:${PATH}" bash -c "fetch devtools-frontend && cd devtools-frontend && gclient sync && gn gen out/Default && autoninja -C out/Default"

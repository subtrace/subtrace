.PHONY: download
download:
	rm subtrace/subtrace-linux-*
	bash -c 'for os in linux; do for arch in amd64 arm64; do curl -L "https://subtrace.dev/download/latest/$$os/$$arch/subtrace" -o subtrace/subtrace-$$os-$$arch; chmod +x subtrace/subtrace-$$os-$$arch; done; done'
	sha256sum subtrace/subtrace-linux-*

.PHONY: wheel
wheel:
	rm -f dist/*.whl
	python -m build --wheel .

.PHONY: upload
upload:
	twine upload dist/*

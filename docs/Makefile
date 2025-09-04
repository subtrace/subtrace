.PHONY: all
all:
	docker run -it --rm \
			-p 3000:3000 \
			-v $$PWD:/src/ \
			-v subtrace_docs_node_modules_cache:/root/.npm/ \
			-v subtrace_docs_mintlify_cache:/root/.mintlify/ \
			--entrypoint bash \
			node:20 \
			-c "cd /src && npx -y mintlify dev"

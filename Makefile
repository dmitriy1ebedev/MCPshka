.PHONY: verify build-matrix

verify:
	@/usr/local/bin/mcpshka verify-production

build-matrix:
	@python3 packaging/lab/build_matrix.py

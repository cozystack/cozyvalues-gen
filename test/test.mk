GENERATOR ?= cozyvalues-gen

generate:
	$(GENERATOR) -v values.yaml -s values.schema.json -r README.md

test: generate
    # check git diff, if empty, exit 0
    # if not empty, print outputs and exit 1
	@git diff --quiet || { git --no-pager diff; exit 1; }
MAKEFLAGS+=--always-make

.PHONY: subtrace
subtrace:
	@bash make.sh subtrace

.SECONDEXPANSION:
%:
	@bash make.sh $*

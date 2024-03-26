all:
	GOEXPERIMENT=rangefunc go build ./cmd/labomatic
	sudo ./labomatic -d testdata/lab1/ -v

install:
	GOEXPERIMENT=rangefunc go install ./cmd/labomatic
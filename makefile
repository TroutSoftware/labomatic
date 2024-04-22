pkg:
	GOEXPERIMENT=rangefunc go build ./cmd/labomatic
	nfpm -p deb package
all:
	go build ./cmd/labomatic
	sudo ./labomatic -d testdata/lab1/ -v

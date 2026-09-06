.PHONY: test build smoke clean

test:
	go test ./...

build:
	mkdir -p bin
	go build -o bin/abcp ./cmd/abcp

smoke: build
	./bin/abcp version
	./bin/abcp validate-transition IMPLEMENTATION_COMPLETED BRANCH_ACCEPTANCE_PENDING
	@if ./bin/abcp validate-transition IMPLEMENTATION_COMPLETED READY_FOR_MERGE; then \
		echo "expected invalid transition to fail"; exit 1; \
	else \
		echo "invalid transition correctly rejected"; \
	fi

clean:
	rm -rf bin dist

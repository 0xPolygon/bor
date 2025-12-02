test:
	go test --timeout 5m -shuffle=on -cover  ./...

test-race:
	go test --timeout 15m -race -shuffle=on  ./...

lint:
	@golangci-lint run --config ./.golangci.yml

lintci-deps:
	rm -f golangci-lint
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b ./ v1.46.0

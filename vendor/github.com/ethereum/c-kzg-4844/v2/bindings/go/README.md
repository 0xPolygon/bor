# Go Bindings for the C-KZG Library

This directory contains Go bindings for the C-KZG-4844 library.

## Prerequisites

This package requires `1.19rc1` or later. Version `1.19beta1` and before will
not work. These versions have a linking issue and are unable to see `blst`
functions.

## Installation

```
go get github.com/ethereum/c-kzg-4844/v2
```

## Tests

Run the tests with this command:
```
go test
```

## Benchmarks

Run the benchmarks with this command:
```
go test -bench=Benchmark
```

## Note

The `go.mod` and `go.sum` files are in the project's root directory because the
bindings need access to the c-kzg-4844 source, but Go cannot reference files
outside its module/package. The best way to deal with this is to make the whole
project available, that way everything is accessible.

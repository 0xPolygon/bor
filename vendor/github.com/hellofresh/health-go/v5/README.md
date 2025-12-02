# health-go

[![Go Report Card](https://goreportcard.com/badge/github.com/hellofresh/health-go)](https://goreportcard.com/report/github.com/hellofresh/health-go)
[![Go Doc](https://godoc.org/github.com/hellofresh/health-go?status.svg)](https://godoc.org/github.com/hellofresh/health-go)

* Exposes an HTTP handler that retrieves health status of the application
* Implements some generic checkers for the following services:
  * RabbitMQ
  * PostgreSQL
  * Redis
  * HTTP
  * MongoDB
  * MySQL
  * gRPC
  * Memcached
  * InfluxDB
  * Nats

## Usage

The library exports `Handler` and `HandlerFunc` functions which are fully compatible with `net/http`.

Additionally, library exports `Measure` function that returns summary status for all the registered health checks,
so it can be used in non-HTTP environments.

### Handler

```go
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/hellofresh/health-go/v5"
	healthMysql "github.com/hellofresh/health-go/v5/checks/mysql"
)

func main() {
	// add some checks on instance creation
	h, _ := health.New(health.WithComponent(health.Component{
		Name:    "myservice",
		Version: "v1.0",
	}), health.WithChecks(health.Config{
		Name:      "rabbitmq",
		Timeout:   time.Second * 5,
		SkipOnErr: true,
		Check: func(ctx context.Context) error {
			// rabbitmq health check implementation goes here
			return nil
		}}, health.Config{
		Name: "mongodb",
		Check: func(ctx context.Context) error {
			// mongo_db health check implementation goes here
			return nil
		},
	},
	))

	// and then add some more if needed
	h.Register(health.Config{
		Name:      "mysql",
		Timeout:   time.Second * 2,
		SkipOnErr: false,
		Check: healthMysql.New(healthMysql.Config{
			DSN: "test:test@tcp(0.0.0.0:31726)/test?charset=utf8",
		}),
	})

	http.Handle("/status", h.Handler())
	http.ListenAndServe(":3000", nil)
}
```

### HandlerFunc
```go
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi"
	"github.com/hellofresh/health-go/v5"
	healthMysql "github.com/hellofresh/health-go/v5/checks/mysql"
)

func main() {
	// add some checks on instance creation
	h, _ := health.New(health.WithComponent(health.Component{
		Name:    "myservice",
		Version: "v1.0",
	}), health.WithChecks(health.Config{
		Name:      "rabbitmq",
		Timeout:   time.Second * 5,
		SkipOnErr: true,
		Check: func(ctx context.Context) error {
			// rabbitmq health check implementation goes here
			return nil
		}}, health.Config{
		Name: "mongodb",
		Check: func(ctx context.Context) error {
			// mongo_db health check implementation goes here
			return nil
		},
	},
	))

	// and then add some more if needed
	h.Register(health.Config{
		Name:      "mysql",
		Timeout:   time.Second * 2,
		SkipOnErr: false,
		Check: healthMysql.New(healthMysql.Config{
			DSN: "test:test@tcp(0.0.0.0:31726)/test?charset=utf8",
		}),
	})

	r := chi.NewRouter()
	r.Get("/status", h.HandlerFunc)
	http.ListenAndServe(":3000", nil)
}
```

For more examples please check [here](https://github.com/hellofresh/health-go/blob/master/_examples/server.go)
## API Documentation

### `GET /status`

Get the health of the application.
- Method: `GET`
- Endpoint: `/status`
- Request:
```
curl localhost:3000/status
```
- Response:

HTTP/1.1 200 OK
```json
{
  "status": "OK",
  "timestamp": "2017-01-01T00:00:00.413567856+033:00",
  "system": {
    "version": "go1.8",
    "goroutines_count": 4,
    "total_alloc_bytes": 21321,
    "heap_objects_count": 21323,
    "alloc_bytes": 234523
  },
  "component": {
    "name": "myservice",
    "version": "v1.0"
  }
}
```

HTTP/1.1 200 OK
```json
{
  "status": "Partially Available",
  "timestamp": "2017-01-01T00:00:00.413567856+033:00",
  "failures": {
    "rabbitmq": "Failed during rabbitmq health check"
  },
  "system": {
    "version": "go1.8",
    "goroutines_count": 4,
    "total_alloc_bytes": 21321,
    "heap_objects_count": 21323,
    "alloc_bytes": 234523
  },
  "component": {
    "name": "myservice",
    "version": "v1.0"
  }
}
```

HTTP/1.1 503 Service Unavailable
```json
{
  "status": "Unavailable",
  "timestamp": "2017-01-01T00:00:00.413567856+033:00",
  "failures": {
    "mongodb": "Failed during mongodb health check"
  },
  "system": {
    "version": "go1.8",
    "goroutines_count": 4,
    "total_alloc_bytes": 21321,
    "heap_objects_count": 21323,
    "alloc_bytes": 234523
  },
  "component": {
    "name": "myservice",
    "version": "v1.0"
  }
}
```

## Contributing
- Fork it
- Create your feature branch (`git checkout -b my-new-feature`)
- Commit your changes (`git commit -am 'Add some feature'`)
- Push to the branch (`git push origin my-new-feature`)
- Create new Pull Request

---
> GitHub [@hellofresh](https://github.com/hellofresh) &nbsp;&middot;&nbsp;
> Medium [@engineering.hellofresh](https://engineering.hellofresh.com)

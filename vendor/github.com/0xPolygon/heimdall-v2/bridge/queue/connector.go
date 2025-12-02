package queue

import (
	"cosmossdk.io/log"
	"github.com/RichardKnop/machinery/v1"
	"github.com/RichardKnop/machinery/v1/config"
	"github.com/streadway/amqp"
)

// The Connector is used to connect to the queue
type Connector struct {
	logger log.Logger
	Server *machinery.Server
}

const (
	// QName is machinery task queue
	QName = "machinery_tasks"
)

// NewQueueConnector creates a new queue connector
func NewQueueConnector(dialer string) *Connector {
	// amqp dialer
	_, err := amqp.Dial(dialer)
	if err != nil {
		panic(err)
	}

	cnf := &config.Config{
		Broker:        dialer,
		DefaultQueue:  QName,
		ResultBackend: dialer,
		AMQP: &config.AMQPConfig{
			Exchange:     "machinery_exchange",
			ExchangeType: "direct",
			BindingKey:   "machinery_task",
		},
	}

	server, err := machinery.NewServer(cnf)
	if err != nil {
		panic(err)
	}

	connector := Connector{
		logger: log.NewNopLogger().With("module", "QueueConnector"),
		Server: server,
	}

	return &connector
}

// StartWorker - starts worker to process registered tasks
func (qc *Connector) StartWorker() {
	worker := qc.Server.NewWorker("invoke-processor", 10)

	qc.logger.Info("Starting machinery worker")

	errors := make(chan error)

	worker.LaunchAsync(errors)
}

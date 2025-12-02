package util

// NoopLogger no operational logger
type NoopLogger struct{}

func (NoopLogger) Print(...interface{})          {}
func (NoopLogger) Printf(string, ...interface{}) {}
func (NoopLogger) Println(...interface{})        {}
func (NoopLogger) Fatal(...interface{})          {}
func (NoopLogger) Fatalf(string, ...interface{}) {}
func (NoopLogger) Fatalln(...interface{})        {}
func (NoopLogger) Panic(...interface{})          {}
func (NoopLogger) Panicf(string, ...interface{}) {}
func (NoopLogger) Panicln(...interface{})        {}

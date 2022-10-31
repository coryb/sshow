package sshow

import (
	"context"
	"log"
)

type Logger interface {
	Printf(string, ...any)
	Errorf(string, ...any)
	Debugf(string, ...any)
}

type defaultLogger struct {
	*log.Logger
}

func (l *defaultLogger) Errorf(format string, v ...any) {
	l.Printf("[ERROR] "+format, v...)
}
func (l *defaultLogger) Debugf(format string, v ...any) {}

type DebugLogger struct {
	Logger
}

func (l DebugLogger) Debugf(format string, v ...any) {
	l.Printf("[DEBUG] "+format, v...)
}

type loggerContextID struct{}

func WithLogger(ctx context.Context, l Logger) context.Context {
	return context.WithValue(ctx, loggerContextID{}, l)
}

func Log(ctx context.Context) Logger {
	logger, ok := ctx.Value(loggerContextID{}).(Logger)
	if !ok {
		return &defaultLogger{log.Default()}
	}
	return logger
}

type stunServerContextID struct{}

func withSTUNServer(ctx context.Context, s string) context.Context {
	return context.WithValue(ctx, stunServerContextID{}, s)
}

func stunServer(ctx context.Context) string {
	s, ok := ctx.Value(stunServerContextID{}).(string)
	if !ok {
		panic("stunServer unset, withSTUNServer not called")
	}
	return s
}

type signalServerContextID struct{}

func withSignalServer(ctx context.Context, s string) context.Context {
	return context.WithValue(ctx, signalServerContextID{}, s)
}

func signalServer(ctx context.Context) string {
	s, ok := ctx.Value(signalServerContextID{}).(string)
	if !ok {
		panic("signalServer unset, withSignalServer not called")
	}
	return s
}

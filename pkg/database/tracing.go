package database

import (
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

const (
	tracingPluginName = "otel-tracing"
	spanInstanceKey   = "otel:span"
	tracerName        = "github.com/supernurture/go-template/pkg/database"
)

// NewTracingPlugin wraps each statement in an OTel client span holding the SQL's placeholders, never its
// values; system is db.system.name. Unlike gorm.io/plugin/opentelemetry it links no ClickHouse or MySQL driver.
func NewTracingPlugin(provider trace.TracerProvider, system string) gorm.Plugin {
	return &tracingPlugin{tracer: provider.Tracer(tracerName), system: system}
}

type tracingPlugin struct {
	tracer trace.Tracer
	system string
}

func (*tracingPlugin) Name() string { return tracingPluginName }

func (p *tracingPlugin) Initialize(db *gorm.DB) error {
	cb := db.Callback()
	return errors.Join(
		cb.Create().Before("gorm:create").Register("otel:before_create", p.before("INSERT")),
		cb.Create().After("gorm:create").Register("otel:after_create", p.after),
		cb.Query().Before("gorm:query").Register("otel:before_query", p.before("SELECT")),
		cb.Query().After("gorm:query").Register("otel:after_query", p.after),
		cb.Update().Before("gorm:update").Register("otel:before_update", p.before("UPDATE")),
		cb.Update().After("gorm:update").Register("otel:after_update", p.after),
		cb.Delete().Before("gorm:delete").Register("otel:before_delete", p.before("DELETE")),
		cb.Delete().After("gorm:delete").Register("otel:after_delete", p.after),
		cb.Row().Before("gorm:row").Register("otel:before_row", p.before("SELECT")),
		cb.Row().After("gorm:row").Register("otel:after_row", p.after),
		// Raw SQL can be any statement, so the operation stays unnamed rather than guessed.
		cb.Raw().Before("gorm:raw").Register("otel:before_raw", p.before("")),
		cb.Raw().After("gorm:raw").Register("otel:after_raw", p.after),
	)
}

func (p *tracingPlugin) before(operation string) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		attrs := []attribute.KeyValue{attribute.String("db.system.name", p.system)}
		if operation != "" {
			attrs = append(attrs, attribute.String("db.operation.name", operation))
		}
		// Statement.Context is left alone: a session reuses its Statement, and the next query would
		// otherwise start under this span after it ended.
		_, span := p.tracer.Start(tx.Statement.Context, spanName(operation, tx.Statement.Table, p.system),
			trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
		tx.InstanceSet(spanInstanceKey, span)
	}
}

func (*tracingPlugin) after(tx *gorm.DB) {
	value, _ := tx.InstanceGet(spanInstanceKey)
	span, ok := value.(trace.Span)
	if !ok {
		return
	}
	defer span.End()

	if query := tx.Statement.SQL.String(); query != "" {
		span.SetAttributes(attribute.String("db.query.text", query))
	}
	if tx.Statement.Table != "" {
		span.SetAttributes(attribute.String("db.collection.name", tx.Statement.Table))
	}
	// A First that finds nothing is an answer, not a failure.
	if err := tx.Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

// spanName follows the convention's "{operation} {table}", falling back to the system for raw SQL.
func spanName(operation, table, system string) string {
	switch {
	case operation != "" && table != "":
		return operation + " " + table
	case operation != "":
		return operation
	default:
		return system
	}
}

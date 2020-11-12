package senders

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wavefronthq/wavefront-sdk-go/event"
	"github.com/wavefronthq/wavefront-sdk-go/histogram"
	"github.com/wavefronthq/wavefront-sdk-go/internal"
	"github.com/wavefronthq/wavefront-sdk-go/version"
)

const (
	metricHandler int = iota
	histoHandler
	spanHandler
	eventHandler
	handlersCount
)

type proxySender struct {
	handlers         []internal.ConnectionHandler
	defaultSource    string
	internalRegistry *internal.MetricRegistry

	//Metrics for Metric Point Handler
	pointsValid			*internal.DeltaCounter
	pointsInvalid		*internal.DeltaCounter
	pointsDropped		*internal.DeltaCounter
	pointReportErrors	*internal.DeltaCounter

	//Metrics for Histogram Distribution Ingestion
	histogramsValid		*internal.DeltaCounter
	histogramsInvalid	*internal.DeltaCounter
	histogramsDropped	*internal.DeltaCounter
	histogramReportErrors	*internal.DeltaCounter

	//Metrics for Tracing Span Ingestion
	spansValid			*internal.DeltaCounter
	spansInvalid		*internal.DeltaCounter
	spansDropped		*internal.DeltaCounter
	spanReportErrors	*internal.DeltaCounter

	spanLogsValid		*internal.DeltaCounter
	spanLogsInvalid		*internal.DeltaCounter
	spanLogsDropped		*internal.DeltaCounter
	spanLogReportErrors *internal.DeltaCounter
}

// Creates and returns a Wavefront Proxy Sender instance
// Deprecated: Use 'senders.NewSender(url)'
func NewProxySender(cfg *ProxyConfiguration) (Sender, error) {
	sender := &proxySender{
		defaultSource: internal.GetHostname("wavefront_proxy_sender"),
		handlers:      make([]internal.ConnectionHandler, handlersCount),
	}

	if cfg.FlushIntervalSeconds == 0 {
		cfg.FlushIntervalSeconds = defaultProxyFlushInterval
	}

	if cfg.MetricsPort != 0 {
		sender.handlers[metricHandler] = makeConnHandler(cfg.Host, cfg.MetricsPort, cfg.FlushIntervalSeconds)
	}

	if cfg.DistributionPort != 0 {
		sender.handlers[histoHandler] = makeConnHandler(cfg.Host, cfg.DistributionPort, cfg.FlushIntervalSeconds)
	}

	if cfg.TracingPort != 0 {
		sender.handlers[spanHandler] = makeConnHandler(cfg.Host, cfg.TracingPort, cfg.FlushIntervalSeconds)
	}

	if cfg.EventsPort != 0 {
		sender.handlers[eventHandler] = makeConnHandler(cfg.Host, cfg.EventsPort, cfg.FlushIntervalSeconds)
	}

	sender.internalRegistry = internal.NewMetricRegistry(
		sender,
		internal.SetPrefix("~sdk.go.core.sender.proxy"),
		internal.SetTag("pid", strconv.Itoa(os.Getpid())),
	)

	if sdkVersion, e := internal.GetSemVer(version.Version); e == nil {
		sender.internalRegistry.NewGaugeFloat64("version", func() float64 {
			return sdkVersion
		})
	}

	sender.pointsValid = sender.internalRegistry.NewDeltaCounter("points.valid")
	sender.pointsInvalid = sender.internalRegistry.NewDeltaCounter("points.invalid")
	sender.pointsInvalid = sender.internalRegistry.NewDeltaCounter("points.dropped")
	sender.pointsInvalid = sender.internalRegistry.NewDeltaCounter("points.report.errors")

	sender.histogramsValid = sender.internalRegistry.NewDeltaCounter("histograms.valid")
	sender.histogramsInvalid = sender.internalRegistry.NewDeltaCounter("histograms.invalid")
	sender.histogramsDropped = sender.internalRegistry.NewDeltaCounter("histograms.dropped")
	sender.histogramReportErrors = sender.internalRegistry.NewDeltaCounter("histograms.report.errors")

	sender.spansValid = sender.internalRegistry.NewDeltaCounter("spans.valid")
	sender.spansInvalid = sender.internalRegistry.NewDeltaCounter("spans.invalid")
	sender.spansDropped = sender.internalRegistry.NewDeltaCounter("spans.dropped")
	sender.spanReportErrors = sender.internalRegistry.NewDeltaCounter("spans.report.errors")

	sender.spanLogsValid = sender.internalRegistry.NewDeltaCounter("span_logs.valid")
	sender.spanLogsInvalid = sender.internalRegistry.NewDeltaCounter("span_logs.invalid")
	sender.spanLogsDropped = sender.internalRegistry.NewDeltaCounter("span_logs.dropped")
	sender.spanLogReportErrors = sender.internalRegistry.NewDeltaCounter("span_logs.report.errors")

	for _, h := range sender.handlers {
		if h != nil {
			sender.Start()
			return sender, nil
		}
	}

	return nil, errors.New("at least one proxy port should be enabled")
}

func makeConnHandler(host string, port, flushIntervalSeconds int) internal.ConnectionHandler {
	addr := host + ":" + strconv.FormatInt(int64(port), 10)
	flushInterval := time.Second * time.Duration(flushIntervalSeconds)
	return internal.NewProxyConnectionHandler(addr, flushInterval)
}

func (sender *proxySender) Start() {
	for _, h := range sender.handlers {
		if h != nil {
			h.Start()
		}
	}
	sender.internalRegistry.Start()
}

func (sender *proxySender) SendMetric(name string, value float64, ts int64, source string, tags map[string]string) error {
	handler := sender.handlers[metricHandler]
	if handler == nil {
		return errors.New("proxy metrics port not provided, cannot send metric data")
	}

	if !handler.Connected() {
		if err := handler.Connect(); err != nil {
			return err
		}
	}

	line, err := MetricLine(name, value, ts, source, tags, sender.defaultSource)
	if err != nil {
		return err
	}
	err = handler.SendData(line)
	return err
}

func (sender *proxySender) SendDeltaCounter(name string, value float64, ts int64, source string, tags map[string]string) error {
	if name == "" {
		return errors.New("empty metric name")
	}
	if !internal.HasDeltaPrefix(name) {
		name = internal.DeltaCounterName(name)
	}
	if value > 0 {
		return sender.SendMetric(name, value, ts, source, tags)
	}
	return nil
}

func (sender *proxySender) SendDistribution(name string, centroids []histogram.Centroid, hgs map[histogram.Granularity]bool, ts int64, source string, tags map[string]string) error {
	handler := sender.handlers[histoHandler]
	if handler == nil {
		return errors.New("proxy distribution port not provided, cannot send distribution data")
	}

	if !handler.Connected() {
		if err := handler.Connect(); err != nil {
			return err
		}
	}

	line, err := HistoLine(name, centroids, hgs, ts, source, tags, sender.defaultSource)
	if err != nil {
		return err
	}
	err = handler.SendData(line)
	return err
}

func (sender *proxySender) SendSpan(name string, startMillis, durationMillis int64, source, traceId, spanId string, parents, followsFrom []string, tags []SpanTag, spanLogs []SpanLog) error {
	handler := sender.handlers[spanHandler]
	if handler == nil {
		return errors.New("proxy tracing port not provided, cannot send span data")
	}

	if !handler.Connected() {
		if err := handler.Connect(); err != nil {
			return err
		}
	}

	line, err := SpanLine(name, startMillis, durationMillis, source, traceId, spanId, parents, followsFrom, tags, spanLogs, sender.defaultSource)
	if err != nil {
		return err
	}
	err = handler.SendData(line)
	if err != nil {
		return err
	}

	if len(spanLogs) > 0 {
		logs, err := SpanLogJSON(traceId, spanId, spanLogs)
		if err != nil {
			return err
		}
		return handler.SendData(logs)
	}
	return nil
}

func (sender *proxySender) SendEvent(name string, startMillis, endMillis int64, source string, tags map[string]string, setters ...event.Option) error {
	handler := sender.handlers[eventHandler]
	if handler == nil {
		return errors.New("proxy events port not provided, cannot send events data")
	}

	if !handler.Connected() {
		if err := handler.Connect(); err != nil {
			return err
		}
	}

	line, err := EventLine(name, startMillis, endMillis, source, tags, setters...)
	if err != nil {
		return err
	}
	err = handler.SendData(line)
	return err
}

func (sender *proxySender) Close() {
	for _, h := range sender.handlers {
		if h != nil {
			h.Close()
		}
	}
	sender.internalRegistry.Stop()
}

func (sender *proxySender) Flush() error {
	errStr := ""
	for _, h := range sender.handlers {
		if h != nil {
			err := h.Flush()
			if err != nil {
				errStr = errStr + err.Error() + "\n"
			}
		}
	}
	if errStr != "" {
		return errors.New(strings.Trim(errStr, "\n"))
	}
	return nil
}

func (sender *proxySender) GetFailureCount() int64 {
	var failures int64
	for _, h := range sender.handlers {
		if h != nil {
			failures += h.GetFailureCount()
		}
	}
	return failures
}

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package fileexporter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/open-telemetry/opentelemetry-collector-contrib/internal/coreinternal/testdata"
)

func buildUnCompressor(compressor string) func([]byte) ([]byte, error) {
	if compressor == compressionZSTD {
		return decompress
	}
	return func(src []byte) ([]byte, error) {
		return src, nil
	}
}

func TestFileTracesExporter(t *testing.T) {
	type args struct {
		conf        *Config
		unmarshaler ptrace.Unmarshaler
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "json: default configuration",
			args: args{
				conf: &Config{
					Path:       tempFileName(t),
					FormatType: "json",
				},
				unmarshaler: &ptrace.JSONUnmarshaler{},
			},
		},
		{
			name: "json: compression configuration",
			args: args{
				conf: &Config{
					Path:        tempFileName(t),
					FormatType:  "json",
					Compression: compressionZSTD,
				},
				unmarshaler: &ptrace.JSONUnmarshaler{},
			},
		},
		{
			name: "Proto: default configuration",
			args: args{
				conf: &Config{
					Path:       tempFileName(t),
					FormatType: "proto",
				},
				unmarshaler: &ptrace.ProtoUnmarshaler{},
			},
		},
		{
			name: "Proto: compression configuration",
			args: args{
				conf: &Config{
					Path:        tempFileName(t),
					FormatType:  "proto",
					Compression: compressionZSTD,
				},
				unmarshaler: &ptrace.ProtoUnmarshaler{},
			},
		},
		{
			name: "Proto: compression configuration--rotation",
			args: args{
				conf: &Config{
					Path:        tempFileName(t),
					FormatType:  "proto",
					Compression: compressionZSTD,
					Rotation: &Rotation{
						MaxMegabytes: 3,
						MaxDays:      0,
						MaxBackups:   defaultMaxBackups,
						LocalTime:    false,
					},
				},
				unmarshaler: &ptrace.ProtoUnmarshaler{},
			},
		},
		{
			name: "Proto: compression configuration--rotation--flush_interval",
			args: args{
				conf: &Config{
					Path:        tempFileName(t),
					FormatType:  "proto",
					Compression: compressionZSTD,
					Rotation: &Rotation{
						MaxMegabytes: 3,
						MaxDays:      0,
						MaxBackups:   defaultMaxBackups,
						LocalTime:    false,
					},
					FlushInterval: time.Second,
				},
				unmarshaler: &ptrace.ProtoUnmarshaler{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := tt.args.conf
			feI := newFileExporter(conf)
			require.IsType(t, &fileExporter{}, feI)
			fe := feI.(*fileExporter)

			td := testdata.GenerateTracesTwoSpansSameResource()
			assert.NoError(t, fe.Start(context.Background(), componenttest.NewNopHost()))
			assert.NoError(t, fe.consumeTraces(context.Background(), td))
			assert.NoError(t, fe.consumeTraces(context.Background(), td))
			defer func() {
				assert.NoError(t, fe.Shutdown(context.Background()))
			}()

			fi, err := os.Open(fe.writer.path)
			assert.NoError(t, err)
			defer fi.Close()
			br := bufio.NewReader(fi)
			for {
				buf, isEnd, err := func() ([]byte, bool, error) {
					if fe.marshaller.formatType == formatTypeJSON && fe.marshaller.compression == "" {
						return readJSONMessage(br)
					}
					return readMessageFromStream(br)
				}()
				assert.NoError(t, err)
				if isEnd {
					break
				}
				decoder := buildUnCompressor(fe.marshaller.compression)
				buf, err = decoder(buf)
				assert.NoError(t, err)
				got, err := tt.args.unmarshaler.UnmarshalTraces(buf)
				assert.NoError(t, err)
				assert.EqualValues(t, td, got)
			}
		})
	}
}

func TestFileTracesExporterError(t *testing.T) {
	mf := &errorWriter{}
	fe := &fileExporter{
		marshaller: &marshaller{
			formatType:      formatTypeJSON,
			tracesMarshaler: tracesMarshalers[formatTypeJSON],
			compressor:      noneCompress,
		},
		writer: &fileWriter{
			file:     mf,
			exporter: exportMessageAsLine,
		},
	}
	require.NotNil(t, fe)

	td := testdata.GenerateTracesTwoSpansSameResource()
	// Cannot call Start since we inject directly the WriterCloser.
	assert.Error(t, fe.consumeTraces(context.Background(), td))
	assert.NoError(t, fe.Shutdown(context.Background()))
}

func TestFileMetricsExporter(t *testing.T) {
	type args struct {
		conf        *Config
		unmarshaler pmetric.Unmarshaler
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "json: default configuration",
			args: args{
				conf: &Config{
					Path:       tempFileName(t),
					FormatType: "json",
				},
				unmarshaler: &pmetric.JSONUnmarshaler{},
			},
		},
		{
			name: "json: compression configuration",
			args: args{
				conf: &Config{
					Path:        tempFileName(t),
					FormatType:  "json",
					Compression: compressionZSTD,
				},
				unmarshaler: &pmetric.JSONUnmarshaler{},
			},
		},
		{
			name: "Proto: default configuration",
			args: args{
				conf: &Config{
					Path:       tempFileName(t),
					FormatType: "proto",
				},
				unmarshaler: &pmetric.ProtoUnmarshaler{},
			},
		},
		{
			name: "Proto: compression configuration",
			args: args{
				conf: &Config{
					Path:        tempFileName(t),
					FormatType:  "proto",
					Compression: compressionZSTD,
				},
				unmarshaler: &pmetric.ProtoUnmarshaler{},
			},
		},
		{
			name: "Proto: compression configuration--rotation",
			args: args{
				conf: &Config{
					Path:        tempFileName(t),
					FormatType:  "proto",
					Compression: compressionZSTD,
					Rotation: &Rotation{
						MaxMegabytes: 3,
						MaxDays:      0,
						MaxBackups:   defaultMaxBackups,
						LocalTime:    false,
					},
				},
				unmarshaler: &pmetric.ProtoUnmarshaler{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := tt.args.conf
			fe := &fileExporter{
				conf: conf,
			}
			require.NotNil(t, fe)

			md := testdata.GenerateMetricsTwoMetrics()
			assert.NoError(t, fe.Start(context.Background(), componenttest.NewNopHost()))
			assert.NoError(t, fe.consumeMetrics(context.Background(), md))
			assert.NoError(t, fe.consumeMetrics(context.Background(), md))
			defer func() {
				assert.NoError(t, fe.Shutdown(context.Background()))
			}()

			fi, err := os.Open(fe.writer.path)
			assert.NoError(t, err)
			defer fi.Close()
			br := bufio.NewReader(fi)
			for {
				buf, isEnd, err := func() ([]byte, bool, error) {
					if fe.marshaller.formatType == formatTypeJSON &&
						fe.marshaller.compression == "" {
						return readJSONMessage(br)
					}
					return readMessageFromStream(br)
				}()
				assert.NoError(t, err)
				if isEnd {
					break
				}
				decoder := buildUnCompressor(fe.marshaller.compression)
				buf, err = decoder(buf)
				assert.NoError(t, err)
				got, err := tt.args.unmarshaler.UnmarshalMetrics(buf)
				assert.NoError(t, err)
				assert.EqualValues(t, md, got)
			}
		})
	}

}

func TestFileMetricsExporterError(t *testing.T) {
	mf := &errorWriter{}
	fe := &fileExporter{
		marshaller: &marshaller{
			formatType:       formatTypeJSON,
			metricsMarshaler: metricsMarshalers[formatTypeJSON],
			compressor:       noneCompress,
		},
		writer: &fileWriter{
			file:     mf,
			exporter: exportMessageAsLine,
		},
	}
	require.NotNil(t, fe)

	md := testdata.GenerateMetricsTwoMetrics()
	// Cannot call Start since we inject directly the WriterCloser.
	assert.Error(t, fe.consumeMetrics(context.Background(), md))
	assert.NoError(t, fe.Shutdown(context.Background()))
}

func TestFileLogsExporter(t *testing.T) {
	type args struct {
		conf        *Config
		unmarshaler plog.Unmarshaler
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "json: default configuration",
			args: args{
				conf: &Config{
					Path:       tempFileName(t),
					FormatType: "json",
				},
				unmarshaler: &plog.JSONUnmarshaler{},
			},
		},
		{
			name: "json: compression configuration",
			args: args{
				conf: &Config{
					Path:        tempFileName(t),
					FormatType:  "json",
					Compression: compressionZSTD,
				},
				unmarshaler: &plog.JSONUnmarshaler{},
			},
		},
		{
			name: "Proto: default configuration",
			args: args{
				conf: &Config{
					Path:       tempFileName(t),
					FormatType: "proto",
				},
				unmarshaler: &plog.ProtoUnmarshaler{},
			},
		},
		{
			name: "Proto: compression configuration",
			args: args{
				conf: &Config{
					Path:        tempFileName(t),
					FormatType:  "proto",
					Compression: compressionZSTD,
				},
				unmarshaler: &plog.ProtoUnmarshaler{},
			},
		},
		{
			name: "Proto: compression configuration--rotation",
			args: args{
				conf: &Config{
					Path:        tempFileName(t),
					FormatType:  "proto",
					Compression: compressionZSTD,
					Rotation: &Rotation{
						MaxMegabytes: 3,
						MaxDays:      0,
						MaxBackups:   defaultMaxBackups,
						LocalTime:    false,
					},
				},
				unmarshaler: &plog.ProtoUnmarshaler{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := tt.args.conf
			fe := &fileExporter{
				conf: conf,
			}
			require.NotNil(t, fe)

			ld := testdata.GenerateLogsTwoLogRecordsSameResource()
			assert.NoError(t, fe.Start(context.Background(), componenttest.NewNopHost()))
			assert.NoError(t, fe.consumeLogs(context.Background(), ld))
			assert.NoError(t, fe.consumeLogs(context.Background(), ld))
			defer func() {
				assert.NoError(t, fe.Shutdown(context.Background()))
			}()

			fi, err := os.Open(fe.writer.path)
			assert.NoError(t, err)
			defer fi.Close()
			br := bufio.NewReader(fi)
			for {
				buf, isEnd, err := func() ([]byte, bool, error) {
					if fe.marshaller.formatType == formatTypeJSON && fe.marshaller.compression == "" {
						return readJSONMessage(br)
					}
					return readMessageFromStream(br)
				}()
				assert.NoError(t, err)
				if isEnd {
					break
				}
				decoder := buildUnCompressor(fe.marshaller.compression)
				buf, err = decoder(buf)
				assert.NoError(t, err)
				got, err := tt.args.unmarshaler.UnmarshalLogs(buf)
				assert.NoError(t, err)
				assert.EqualValues(t, ld, got)
			}
		})
	}
}

func TestFileLogsExporterErrors(t *testing.T) {
	mf := &errorWriter{}
	fe := &fileExporter{
		marshaller: &marshaller{
			formatType:    formatTypeJSON,
			logsMarshaler: logsMarshalers[formatTypeJSON],
			compressor:    noneCompress,
		},
		writer: &fileWriter{
			file:     mf,
			exporter: exportMessageAsLine,
		},
	}
	require.NotNil(t, fe)

	ld := testdata.GenerateLogsTwoLogRecordsSameResource()
	// Cannot call Start since we inject directly the WriterCloser.
	assert.Error(t, fe.consumeLogs(context.Background(), ld))
	assert.NoError(t, fe.Shutdown(context.Background()))
}

func TestExportMessageAsBuffer(t *testing.T) {
	path := tempFileName(t)
	fe := &fileExporter{
		marshaller: &marshaller{
			formatType:    formatTypeProto,
			logsMarshaler: logsMarshalers[formatTypeProto],
		},
		writer: &fileWriter{
			path: path,
			file: &lumberjack.Logger{
				Filename: path,
				MaxSize:  1,
			},
			exporter: exportMessageAsBuffer,
		},
	}
	require.NotNil(t, fe)
	//
	ld := testdata.GenerateLogsManyLogRecordsSameResource(15000)
	marshaler := &plog.ProtoMarshaler{}
	buf, err := marshaler.MarshalLogs(ld)
	assert.NoError(t, err)
	assert.Error(t, exportMessageAsBuffer(fe.writer, buf))
	assert.NoError(t, fe.Shutdown(context.Background()))
}

// tempFileName provides a temporary file name for testing.
func tempFileName(t testing.TB) string {
	return filepath.Join(t.TempDir(), "fileexporter_test.tmp")
}

// errorWriter is an io.Writer that will return an error all ways
type errorWriter struct {
}

func (e errorWriter) Write([]byte) (n int, err error) {
	return 0, errors.New("all ways return error")
}

func (e *errorWriter) Close() error {
	return nil
}

func readMessageFromStream(br *bufio.Reader) ([]byte, bool, error) {
	var length int32
	// read length
	err := binary.Read(br, binary.BigEndian, &length)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, true, nil
		}
		return nil, false, err
	}
	buf := make([]byte, length)
	err = binary.Read(br, binary.BigEndian, &buf)
	if err == nil {
		return buf, false, nil
	}
	if errors.Is(err, io.EOF) {
		return nil, true, nil
	}
	return nil, false, err
}

func readJSONMessage(br *bufio.Reader) ([]byte, bool, error) {
	buf, _, c := br.ReadLine()
	if c == io.EOF {
		return nil, true, nil
	}
	return buf, false, nil
}

// Create a reader that caches decompressors.
// For this operation type we supply a nil Reader.
var decoder, _ = zstd.NewReader(nil)

// decompress a buffer.
func decompress(src []byte) ([]byte, error) {
	return decoder.DecodeAll(src, nil)
}

func TestConcurrentlyCompress(t *testing.T) {
	wg := sync.WaitGroup{}
	wg.Add(3)
	var (
		ctd []byte
		cmd []byte
		cld []byte
	)
	td := testdata.GenerateTracesTwoSpansSameResource()
	md := testdata.GenerateMetricsTwoMetrics()
	ld := testdata.GenerateLogsTwoLogRecordsSameResource()
	go func() {
		defer wg.Done()
		buf, err := tracesMarshalers[formatTypeJSON].MarshalTraces(td)
		if err != nil {
			return
		}
		ctd = zstdCompress(buf)
	}()
	go func() {
		defer wg.Done()
		buf, err := metricsMarshalers[formatTypeJSON].MarshalMetrics(md)
		if err != nil {
			return
		}
		cmd = zstdCompress(buf)
	}()
	go func() {
		defer wg.Done()
		buf, err := logsMarshalers[formatTypeJSON].MarshalLogs(ld)
		if err != nil {
			return
		}
		cld = zstdCompress(buf)
	}()
	wg.Wait()
	buf, err := decompress(ctd)
	assert.NoError(t, err)
	traceUnmarshaler := &ptrace.JSONUnmarshaler{}
	got, err := traceUnmarshaler.UnmarshalTraces(buf)
	assert.NoError(t, err)
	assert.EqualValues(t, td, got)

	buf, err = decompress(cmd)
	assert.NoError(t, err)
	metricsUnmarshaler := &pmetric.JSONUnmarshaler{}
	gotMd, err := metricsUnmarshaler.UnmarshalMetrics(buf)
	assert.NoError(t, err)
	assert.EqualValues(t, md, gotMd)

	buf, err = decompress(cld)
	assert.NoError(t, err)
	logsUnmarshaler := &plog.JSONUnmarshaler{}
	gotLd, err := logsUnmarshaler.UnmarshalLogs(buf)
	assert.NoError(t, err)
	assert.EqualValues(t, ld, gotLd)
}

// tsBuffer is a thread safe buffer to prevent race conditions in the CI/CD.
type tsBuffer struct {
	b *bytes.Buffer
	m sync.Mutex
}

func (b *tsBuffer) Write(d []byte) (int, error) {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.Write(d)
}

func (b *tsBuffer) Len() int {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.Len()
}

func (b *tsBuffer) Bytes() []byte {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.Bytes()
}

func safeFileExporterWrite(e *fileExporter, d []byte) (int, error) {
	e.writer.mutex.Lock()
	defer e.writer.mutex.Unlock()
	return e.writer.file.Write(d)
}

func TestFlushing(t *testing.T) {
	cfg := &Config{
		Path:          tempFileName(t),
		FlushInterval: time.Second,
	}

	// Create a buffer to capture the output.
	bbuf := &tsBuffer{b: &bytes.Buffer{}}
	buf := &NopWriteCloser{bbuf}
	// Wrap the buffer with the buffered writer closer that implements flush() method.
	bwc := newBufferedWriteCloser(buf)
	// Create a file exporter with flushing enabled.
	feI := newFileExporter(cfg)
	assert.IsType(t, &fileExporter{}, feI)
	fe := feI.(*fileExporter)

	// Start the flusher.
	ctx := context.Background()
	fe.marshaller = &marshaller{
		formatType:       fe.conf.FormatType,
		tracesMarshaler:  tracesMarshalers[fe.conf.FormatType],
		metricsMarshaler: metricsMarshalers[fe.conf.FormatType],
		logsMarshaler:    logsMarshalers[fe.conf.FormatType],
		compression:      fe.conf.Compression,
		compressor:       buildCompressor(fe.conf.Compression),
	}
	export := buildExportFunc(fe.conf)
	var err error
	fe.writer, err = newFileWriter(fe.conf.Path, fe.conf.Append, fe.conf.Rotation, fe.conf.FlushInterval, export)
	assert.NoError(t, err)
	err = fe.writer.file.Close()
	assert.NoError(t, err)
	fe.writer.file = bwc
	fe.writer.start()

	// Write 10 bytes.
	b := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	i, err := safeFileExporterWrite(fe, b)
	assert.NoError(t, err)
	assert.EqualValues(t, len(b), i, "bytes written")

	// Assert buf contains 0 bytes before flush is called.
	assert.EqualValues(t, 0, bbuf.Len(), "before flush")

	// Wait 1.5 sec
	time.Sleep(1500 * time.Millisecond)

	// Assert buf contains 10 bytes after flush is called.
	assert.EqualValues(t, 10, bbuf.Len(), "after flush")
	// Compare the content.
	assert.EqualValues(t, b, bbuf.Bytes())
	assert.NoError(t, fe.Shutdown(ctx))
}

func TestAppend(t *testing.T) {
	cfg := &Config{
		Path:          tempFileName(t),
		FlushInterval: time.Second,
		Append:        true,
	}

	// Create a buffer to capture the output.
	bbuf := &tsBuffer{b: &bytes.Buffer{}}
	buf := &NopWriteCloser{bbuf}
	// Wrap the buffer with the buffered writer closer that implements flush() method.
	bwc := newBufferedWriteCloser(buf)
	// Create a file exporter with flushing enabled.
	feI := newFileExporter(cfg)
	assert.IsType(t, &fileExporter{}, feI)
	fe := feI.(*fileExporter)

	// Start the flusher.
	ctx := context.Background()
	fe.marshaller = &marshaller{
		formatType:       fe.conf.FormatType,
		tracesMarshaler:  tracesMarshalers[fe.conf.FormatType],
		metricsMarshaler: metricsMarshalers[fe.conf.FormatType],
		logsMarshaler:    logsMarshalers[fe.conf.FormatType],
		compression:      fe.conf.Compression,
		compressor:       buildCompressor(fe.conf.Compression),
	}
	export := buildExportFunc(fe.conf)
	var err error
	fe.writer, err = newFileWriter(fe.conf.Path, fe.conf.Append, fe.conf.Rotation, fe.conf.FlushInterval, export)
	assert.NoError(t, err)
	err = fe.writer.file.Close()
	assert.NoError(t, err)
	fe.writer.file = bwc
	fe.writer.start()

	// Write 10 bytes.
	b1 := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	i, err := safeFileExporterWrite(fe, b1)
	assert.NoError(t, err)
	assert.EqualValues(t, len(b1), i, "bytes written")

	// Assert buf contains 0 bytes before flush is called.
	assert.EqualValues(t, 0, bbuf.Len(), "before flush")

	// Wait 1.5 sec
	time.Sleep(1500 * time.Millisecond)

	// Assert buf contains 10 bytes after flush is called.
	assert.EqualValues(t, 10, bbuf.Len(), "after flush")
	// Compare the content.
	assert.EqualValues(t, b1, bbuf.Bytes())
	assert.NoError(t, fe.Shutdown(ctx))

	// Restart the exporter
	fe.writer, err = newFileWriter(fe.conf.Path, fe.conf.Append, fe.conf.Rotation, fe.conf.FlushInterval, export)
	assert.NoError(t, err)
	err = fe.writer.file.Close()
	assert.NoError(t, err)
	fe.writer.file = bwc
	fe.writer.start()

	// Write 10 bytes - again
	b2 := []byte{11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	i, err = safeFileExporterWrite(fe, b2)
	assert.NoError(t, err)
	assert.EqualValues(t, len(b2), i, "bytes written")

	// Assert buf contains 10 bytes before flush is called.
	assert.EqualValues(t, 10, bbuf.Len(), "after restart - before flush")

	// Wait 1.5 sec
	time.Sleep(1500 * time.Millisecond)

	// Assert buf contains 20 bytes after flush is called.
	assert.EqualValues(t, 20, bbuf.Len(), "after restart - after flush")
	// Compare the content.
	bComplete := slices.Clone(b1)
	bComplete = append(bComplete, b2...)
	assert.EqualValues(t, bComplete, bbuf.Bytes())
	assert.NoError(t, fe.Shutdown(ctx))
}
